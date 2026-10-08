# Design 006: Configurable workload-kind allowlist

Status: Accepted 2026-10-08 (review window on #138); implemented on
main the same week. Tracks #136. Targets the v1.6 train (the roadmap
commitment), with the watch-narrowing half for apps/v1 kinds deferred to
v1.7 for the reason in §3.

Implementation note (2026-10-08): §2's claim that "a config change
enqueues the same way" as a namespace change did not hold — nothing
re-enqueued workloads on a snapshot rotation; the other config fields
apply lazily, on each workload's next event. The allowlist cannot be
lazy (the operator's visible outcome is "the removed kind's documents
are gone"), so the implementation adds a fan-out: on a `workloadKinds`
change every workload of every reporting kind is listed once through
the uncached reader and re-enqueued. Disabled supervised kinds have no
controller left to delete their documents, so the supervisor sweeps
them on the transition into the disabled state. Open question 1 (a
plugin notice when the allowlist is active) is not in this change: the
plugin does not read `AIBOMControllerConfig` today and reader RBAC may
not permit it; tracked separately.

## Context

k8s-aibom reports on every kind it knows how to read: apps/v1
`Deployment`, `StatefulSet`, `DaemonSet`; batch `Job`, `CronJob`; and,
when their CRDs are present, KServe `InferenceService`, Dynamo
`DynamoGraphDeployment` and `DynamoComponentDeployment`, `NIMService`,
`LeaderWorkerSet`. Operators have asked for two things this does not
allow:

1. **Report less.** A platform team that only wants inference services
   inventoried, not every batch Job that happens to pull an evaluation
   image, has no knob today.
2. **Read less.** The controller's informers watch every kind above
   cluster-wide, and the README says so plainly under "Data visibility":
   workload specs pass through the cache before the namespace opt-in
   check. Several reviewers have asked whether a deployment that does
   not need, say, Jobs could avoid the controller ever reading them. The
   roadmap entry for this feature exists because of that question.

The two are different features. The first is a reporting filter and
can be hot-reloaded like every other `AIBOMControllerConfig` field. The
second decides which informers exist, and informers are created when a
controller starts. Conflating them produces either a filter that
pretends to reduce read surface or a watch set that cannot be changed
without a restart while the rest of the config can.

## Goal

One field that an operator reads as "the kinds k8s-aibom inventories,"
honest about which of its two effects apply to which kinds, and safe
for the ownership roll-up (Design 005): removing a kind must never make
a workload silently disappear.

## Non-goals

- Per-namespace kind allowlists. One cluster-wide list.
- Denylists. A list of what is in is easier to audit than a list of
  what is out; "all" is the default.
- Narrowing by anything other than kind (labels, names, images). The
  namespace selector already covers namespace scope.

## Decision

### 1. The field

```yaml
apiVersion: aibom.k8saibom.dev/v1beta1
kind: AIBOMControllerConfig
spec:
  discovery:
    workloadKinds:            # absent or empty = every known kind
      - apps/Deployment
      - apps/StatefulSet
      - nvidia.com/DynamoGraphDeployment
      - leaderworkerset.x-k8s.io/LeaderWorkerSet
```

`spec.discovery.workloadKinds` is a list of `Group/Kind` strings (core
group written as `core/Kind`, unused today). Entries are validated at
load against the compiled-in set of known kinds; an unknown entry is a
config error (`ConfigInvalid`, last-known-good retained), not a silent
no-op, so a typo cannot quietly turn off inventory. Absent or empty
means every known kind, which is today's behavior and the compiled
default.

### 2. Reporting filter: all kinds, hot-reloaded

The parsed set lives on the config `Snapshot` as `WorkloadKinds`
(pre-materialized, like `NamespaceSelector`). `reconcileWorkload`
checks it immediately after the namespace opt-in check: a kind not in
the set is treated exactly like a namespace that is not opted in — no
scrape, any existing AIBOM for the workload deleted, outcome
`kind_not_allowed`. Hot-reload follows the existing snapshot rotation:
adding a kind starts producing documents on the next reconcile;
removing one deletes them on the next reconcile of each workload (the
namespace watch already enqueues every workload in a namespace on
config-relevant changes; a config change enqueues the same way).

### 3. Watch narrowing: third-party kinds now, apps/v1 kinds next train

- **Third-party kinds** run under the Design 004 supervisor, which
  already owns each kind's lifecycle. The supervisor subscribes to
  snapshot changes: a kind removed from the set is stopped (cache and
  controller torn down, exactly as on a watch error, but recorded as
  "disabled by configuration", not Degraded); a kind added is started
  through the normal probe path. **Hot-reloaded, and genuinely reduces
  read surface**, because the informer for a disabled kind does not
  exist.
- **apps/v1 and batch kinds** are controllers on the manager's shared
  cache, created before the manager starts. Their informers cannot be
  removed at runtime. In v1.6 the allowlist filters their *reporting*
  only; the watch stays. The README's "Data visibility" paragraph says
  so explicitly for this release. Moving the apps/v1 kinds under the
  supervisor so their watches can be narrowed too is the v1.7
  follow-up: it is the same mechanism Design 004 built, applied to
  kinds that have no CRD-absent case, and it is deliberately not
  bundled here because it changes the reconcile path for every
  Deployment in every cluster and deserves its own window.

An operator who needs the read-surface reduction for a third-party kind
today gets it. One who needs it for Jobs gets a truthful answer and a
dated follow-up, not a knob that implies something it does not do.

### 4. Interaction with the ownership roll-up (Design 005)

The tracked-kind set used for suppression **follows the allowlist**. A
kind removed from the list stops being a roll-up owner, so the
workloads it used to absorb are reported as roots again on their next
reconcile. The alternative — keeping a disabled kind as a tracked owner
— would make its children vanish along with it, which violates the
"nothing silently disappears" rule the roll-up was built on. The
`rollup_unresolved` path is not involved; this is a clean re-rooting.

The reverse holds too: adding a kind makes it an owner, and children it
owns are absorbed on their next reconcile with their AIBOMs deleted, as
on upgrade to v1.6.

### 5. Schema and skew

A new optional spec field. The served-schema check (#104) already
detects a CRD that predates the controller and reports
`Degraded=True / SchemaPredatesController`; an operator who upgrades
the controller but not the CRD sees that condition rather than a
silently pruned `workloadKinds`. The chart's render-time guard (#119)
covers `config.verification` only; extending it to `workloadKinds` is
not needed because pruning here degrades to "all kinds", which is the
safe direction.

## Degradation

| Situation | Result |
|---|---|
| Unknown `Group/Kind` in the list | `ConfigInvalid`, last-known-good retained, Event names the entry |
| Kind listed but CRD absent | Logged at startup as today; the supervisor never starts it; no Degraded (absence is not failure) |
| Kind removed while its watch is Degraded (Design 004) | Stopped and marked disabled; Degraded clears for that kind |
| Field pruned by an old CRD | Served-schema skew reported; behavior falls back to all kinds |

## Testing

- Unit: parse/validate (known, unknown, duplicate, `core/` spelling);
  snapshot default is "all".
- Envtest: a Deployment in an opted-in namespace with
  `workloadKinds: [apps/StatefulSet]` gets no AIBOM and its existing one
  is deleted; adding `apps/Deployment` back restores it without a
  restart. A supervised kind removed from the list: its informer is
  gone (health status `disabled`, gauge 0, no Warning event), existing
  AIBOMs of that kind are deleted on their next reconcile, its former
  children re-root. Roll-up interaction asserted on the LWS → StatefulSet
  fixture.
- Byte-identity: an empty or absent list produces identical documents.

## Rollout

Additive MINOR on the v1.6 train. CHANGELOG states the apps/v1 watch
caveat and the v1.7 follow-up explicitly.

## Open questions

1. Should `kubectl aibom summary` and `find` print a one-line notice
   when the allowlist is active ("3 of 10 known kinds enabled"), so a
   zero-match result in an incident is not misread? Proposal: yes, on
   stderr, same channel as the truncated-document warning.
