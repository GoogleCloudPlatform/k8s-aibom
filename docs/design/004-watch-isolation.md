# Design 004: Watch isolation for third-party kinds

Status: Draft, 2026-10-02. Fixes #127. Targets the v1.6 train; on the
critical path for the AICR Dynamo-pairing qualification. Review window
stated on the PR; the Dynamo and AICR reviewers already on #127 are
specifically invited.

Amendment for v1.7 (#226), **Draft, not accepted**: third-party CRDs
installed after startup. See the amendment section at the end; the
sections above it describe v1.6.

## Context

k8s-aibom watches four third-party kinds (KServe `InferenceService`,
Dynamo `DynamoGraphDeployment`, NIM `NIMService`, `LeaderWorkerSet`)
through the same controller-runtime manager and the same shared
informer cache as the apps/v1 kinds. Each watch is registered only
when the CRD is present at startup.

Dynamo's deployed CRDs (dynamo-platform ≤ 1.4, AICR's pin) store
`v1alpha1` and convert to the `v1beta1` we read through a conversion
webhook served by the Dynamo operator. If that operator is down, or
was uninstalled with CRs left behind, every `v1beta1` list fails:

```
conversion webhook for nvidia.com/v1alpha1, Kind=DynamoGraphDeployment failed:
Post "https://…/convert": dial tcp …: connection refused
```

## What happens today (measured, envtest, 2026-10-02)

Three experiments against a Dynamo CRD with `v1alpha1` storage,
`v1beta1` served, and the conversion webhook pointed at a closed port,
with a stored `v1alpha1` object and an unrelated vLLM Deployment in an
opted-in namespace (`internal/controller/dynamo_conversion_envtest_test.go`):

| Case | Manager | Unrelated Deployment | Dynamo AIBOMs | Surfaced |
|---|---|---|---|---|
| Control (no webhook) | up | AIBOM in 1.5 s | created | — |
| Webhook down **at startup** | **exits after CacheSyncTimeout** (2 min default) | **no AIBOM, ever** | none | log line blaming `*v1.Deployment` / `*v1.Pod` |
| Webhook dies **after startup** | up | keeps working | existing survive; **new ones silently get nothing** | **zero events, no condition** |

Mechanism (controller-runtime v0.24,
`pkg/internal/source/kind.go:105`): every watch source waits for the
**whole shared cache** to sync. One informer that can never list —
the Dynamo one — makes every controller's cache-sync wait time out,
and each controller reports its *own* kind in the error. In
production `main.go` exits on that error, so a down Dynamo operator
crash-loops all of k8s-aibom, and the log sends the operator to the
wrong kind. After a successful start the reflector's relist failures
go to a watch-error handler that today only logs, so the cache goes
stale in silence.

The second row is the one that matters for AICR: their recipes run
k8s-aibom and Dynamo side by side, and an operator upgrade or restart
is routine.

## Goal

A failing third-party watch is a **per-kind degradation**: visible,
bounded, self-healing, and incapable of affecting any other kind or
the controller's own liveness.

## Non-goals

- Changing how the apps/v1 kinds (Deployment, StatefulSet, DaemonSet,
  Job, CronJob) are watched. They stay on the manager's shared cache.
- Watching third-party CRDs that are installed *after* the controller
  starts (v1.6: restart required). This non-goal is lifted for v1.7 by
  the amendment at the end of this document (#226).
- Any behavior that touches a third-party CRD (Design 003 docs,
  `docs/external-crd-versions.md` invariant).

## Decision

### 1. One cache per third-party kind, outside the manager's cache

Each third-party kind gets its own `cache.Cache` (controller-runtime
`cache.New`) restricted to that GVK plus the `Namespace` informer it
needs for opt-in handling. The manager's shared cache never contains
third-party kinds, so nothing in it can wait on them.

### 2. Unmanaged controllers under a supervisor

Each third-party reconciler is built with `controller.NewUnmanaged`
and sources from its own cache. A `watchSupervisor` runnable (added to
the manager once, non-leader-election-gated like the other
controllers) owns the lifecycle:

- **Probe before start.** A `List` with `Limit: 1` through the
  manager's API reader. Success → start the cache and controller.
  Failure → record the error in the health registry, do not start, and
  retry with capped exponential backoff (5 s → 5 min).
- **Never return an error to the manager.** A cache-sync timeout or
  controller start error is recorded, the cache and controller are torn
  down (cancel their context), and the kind re-enters the probe loop.
  The manager, and every other kind, never see it.
- **Steady-state health.** The per-kind cache is created with a
  `DefaultWatchErrorHandler` that records list/relist errors in the
  health registry. That alone is **not** enough for the
  webhook-dies-after-startup case, measured during implementation:
  when an established watch stream hits a conversion error, client-go's
  reflector does not call the error handler — it logs at warning level
  and re-opens the watch from the same resource version, forever, so
  the new object is never delivered and nothing fails loudly. The
  supervisor therefore probes every kind on a fixed interval (default
  2 min; one `Limit: 1` list per kind, which takes the same conversion
  path): a failing probe marks the kind unhealthy, a clean probe clears
  it. Cost with AICR's two kinds: one extra request per minute on top
  of the measured sub-1-req/min steady state.
- **Amendment (2026-10-22, #216).** The steady-state probe is a one-item
  list **followed by a GET of that item by name**. On GKE 1.35 with
  dynamo-platform 1.4.2, a 7-minute operator outage never failed the
  Limit=1 list: the API server serves LIST and WATCH for a served
  version from its per-version watch cache while the conversion webhook
  is down, and only storage reads (single-object GET) and writes reach
  the webhook. The GET restores the honest failure; a kind with no
  objects falls back to the list. Evidence:
  `docs/evidence/v1.6.1-gke-adr019-dynamo.md`, leg 2.

- **Teardown on probe failure after start?** No. A kind that was
  healthy and is now failing its relists keeps serving its last-known
  cache (existing AIBOMs are reconciled from a stale but real view) and
  reports Degraded. Tearing it down would turn a stale view into no
  view. Only a controller-level error (sync timeout on restart) tears
  down.

### 3. Health registry → condition, event, metric

`WatchHealth` is a small concurrency-safe registry: kind → `{healthy
bool, lastError string, since time, consecutiveFailures int}`.

- **Condition.** The `AIBOMControllerConfig` reconciler already owns
  `Degraded`; it gains a `WatchHealth` dependency and sets
  `Degraded=True` with reason `ThirdPartyWatchUnhealthy` and a message
  naming every unhealthy kind and its last error, independent of (and
  additive to) the schema-skew reason. Clears when all kinds are
  healthy. Same channel as #104, so operators learn one place to look.
- **Event.** One Warning event on the controller Pod per transition
  to unhealthy (`WatchUnhealthy`, with the kind and error) and one
  Normal event on recovery (`WatchRecovered`). Not per retry.
- **Metric.** `aibom_watch_healthy{kind}` gauge (1/0) and
  `aibom_watch_errors_total{kind}` counter.
- **Readiness is not affected.** A down Dynamo operator must not make
  k8s-aibom unready; the apps kinds are still being served. This is the
  same reasoning as the schema-skew check staying out of
  `ConfigInvalid()`.

### 4. Existing AIBOMs are never cleaned up because a watch is unhealthy

Stale-report cleanup for a kind runs only from events delivered by a
healthy watch. An unhealthy kind performs no deletions. (Today's
controllers delete via owner references and namespace-opt-out
handling; the supervisor gates the opt-out path for unhealthy kinds.)

## Startup presence check (unchanged, now explicit)

The `RESTMapping` check in `main.go` decides whether a kind is
*registered*. It stays. What changes is that registration now means
"handed to the supervisor", not "added to the manager's cache", so a
CRD that is present but unservable no longer takes the process down.

v1.7 amendment (#226): this check is no longer final; the supervisor
confirms readiness by reading each CRD and starts late-installed
candidates.

## Degradation summary

| Situation | Before | After |
|---|---|---|
| Third-party CRD absent | kind skipped (log) | same |
| CRD present, conversion webhook down at startup | **whole controller crash-loops**, misleading log | kind Degraded, retried with backoff; everything else serves |
| Webhook dies after startup | silent: new CRs invisible, no signal | Degraded + Warning + metric; last-known view kept; auto-recovers |
| Operator uninstalled, CRs left behind | crash-loop on next restart | Degraded until CRs or CRD are removed; everything else serves |

## Testing

- The three experiments above become the regression suite, with
  assertions instead of logs: control healthy; webhook-down-at-startup
  → manager up, Deployment AIBOM created, Dynamo kind Degraded with the
  conversion error in the message, Warning event present, gauge 0;
  webhook-dies-after-startup → Degraded + Warning, existing Dynamo
  AIBOMs present, new Deployment AIBOM created; **recovery**: point the
  conversion back to None → Degraded clears, the late DGD gets its
  AIBOM, Normal event present.
- Unit: supervisor backoff and teardown logic against a fake probe;
  health registry transitions emit exactly one event per transition.
- Byte-identity: no AIBOM document changes; the apps/v1 path is
  untouched.

## Rollout

Additive, v1.6 train. No chart or RBAC changes (the kinds and verbs
are the same; only which cache serves them changes). No new flags.

## Open questions

1. Should KServe move under the supervisor in the same change? Its
   CRD has no conversion webhook today, but the isolation argument is
   identical and keeping two code paths for "third-party kind" is the
   worse outcome. Proposal: yes, all four kinds.
2. Backoff ceiling: 5 minutes means up to 5 minutes between a Dynamo
   operator coming back and k8s-aibom noticing. Acceptable for an
   inventory; shorter is cheap if a reviewer wants it.

## Amendment (#226, v1.7): CRDs installed after startup

Status: **Draft, not accepted.** Proposed 2026-10-24 for the v1.7
train. Review window: three working days from the date the design PR
is opened; implementation starts after it closes. The sections above
describe v1.6 and are unchanged except where they point here.

### Context and scope

Main supervises six candidates: KServe `InferenceService`,
`DynamoGraphDeployment`, `DynamoComponentDeployment`, `NIMService`,
`LeaderWorkerSet` and `DisaggregatedSet`. `RegisterThirdPartyWatches`
maps each once; an absent candidate is dropped for the life of the
process, and with none present the supervisor is not added at all, so a
late operator install needs a restart
(`docs/evidence/v1.6.0-rc1-gke-two-chart-dset.md`, 17:47). Out of
scope: the conversion-webhook probe (#216, done), the apps/v1 kinds
(Design 006 §3), any CRD other than the six, polling, and any CRD write.

### A1. CRD state is separate from the allowlist

Discovery (A3) gives each candidate a CRD state; the allowlist stays
Design 006's `Allowed`/`Excluded`. They are never merged into one
desired set.

| CRD state | Meaning |
|---|---|
| `Unknown` | Never confirmed Ready by this supervisor (absent at start, or present but never Ready) |
| `Ready(uid)` | `Established=True`, `spec.names.kind` and the compiled version (`served: true`) match the compiled GVK, `scope: Namespaced` |
| `Unservable(uid)` | Was Ready, still exists, now fails a Ready condition |
| `Removed(uid)` | Was Ready or Unservable; a GET by name returns NotFound or shows `deletionTimestamp` |

A read error other than NotFound changes nothing; a watch DELETE event
only triggers a read. The destructive path (`MarkDisabled`,
`WatchDisabled`, AIBOM sweep) is reachable only from `Removed`, or from
`Excluded` while the CRD is `Ready`/`Unservable`. `apply()`'s existing
`!allowed → disable()` branch is gated on the CRD having been seen, so
an excluded candidate that was never installed gets no event, health
entry or sweep, as in v1.6.

### A2. Transitions

Each row runs serially on the supervisor goroutine; "stop" means cancel
the runner and wait for it to return before anything else.

| # | Change | Runner | Health / gauge | `TrackedKinds` | Event | AIBOMs |
|---|---|---|---|---|---|---|
| 1 | `Unknown`, either allowlist value | none | not registered | — | none | untouched |
| 2 | `Unknown`/`Removed` → `Ready`, Allowed | start | `Register`, `MarkStarting` → healthy on sync | `Add` first | `WatchStarted` for late discovery; `WatchRecovered` for initial startup (A4) | created by reconciles |
| 3 | `Unknown`/`Removed` → `Ready`, Excluded | none | `MarkDisabled(config)` | `Add` | `WatchDisabled` | swept once |
| 4 | `Ready`, CRD update that stays Ready | unchanged | unchanged | — | none | untouched |
| 5 | `Ready` → `Unservable`, Allowed | stop | `MarkUnhealthy`: Degraded | unchanged | `WatchUnhealthy` | **kept** (§4) |
| 6 | `Unservable` → `Ready`, Allowed | start | healthy on sync | unchanged | `WatchRecovered` | kept |
| 7 | `Ready`/`Unservable`, Allowed → Excluded | stop | `MarkDisabled(config)` | unchanged | `WatchDisabled` | swept once (Design 006) |
| 8 | → `Removed(uid)` | stop | `MarkDisabled(crd-removed)` | `Remove` | `WatchDisabled` on entry only | swept on entry only |
| 9 | Ready under a new UID | row 8 for the old UID, then row 2/3 | — | `Remove`, `Add` | as rows 8, 2/3 | swept before the new runner starts |
| 10 | Excluded → Allowed, `Ready` | start | healthy on sync | — | `WatchRecovered` (unchanged) | recreated |
| 11 | Discovery failure or webhook outage | unchanged | §3 / A6 | — | `WatchUnhealthy` | kept |

`MarkDisabled` gains a reason (`config`, `crd-removed`) recorded in
`WatchStatus` and included in the `WatchDisabled` event message; it
still returns true only on entry, so each sweep runs
once. `TrackedKinds` membership follows CRD presence, not health:
`Remove` only at rows 8–9. After every `Add` or `Remove` the supervisor
runs one allowlist fan-out, so children reconciled as roots before
their owner was tracked are absorbed (Design 006 §4), and children of a
removed owner re-root through `ownerUnresolved` (Design 005).

### A3. Readiness and discovery: one metadata informer

- **Readiness is read, not inferred.** An informer event or
  `RESTMapping` success cannot prove `Established=True` or served
  versions, so each classification GETs the full CRD by compiled
  name through the uncached API reader and verifies A1's conditions.
  CRD reads do not involve conversion webhooks.
- **Startup.** On `Start`, after leader acquisition, independently
  GET each candidate before its first `apply()`. A Ready candidate
  starts through the existing supervisor path. A missing candidate
  has no runner, health entry, event or sweep. Other GET failures
  (including 403 and timeout) are recorded (A6) and retried, never
  interpreted as Ready or Removed. `RegisterThirdPartyWatches`
  always registers the supervisor, even with no CRDs present. Its
  former `RESTMapping` check is not evidence of CRD readiness.
  `TrackedKinds` is updated only after readiness is confirmed.
- **One shared metadata-only informer.** Inside the leader's
  `WatchSupervisor`, outside the manager cache, watch
  `apiextensions.k8s.io/v1 CustomResourceDefinition` metadata using
  one informer, with no resync and no CRD writes. Its LIST/WATCH
  includes CRD metadata across the cluster, but handlers filter by
  the six exact compiled CRD names. Updates (including status-only
  Established changes) and DELETE events enqueue matching names;
  unrelated CRDs are ignored. No six per-name informer caches and
  no controller-runtime multi-source controller. Drop unneeded
  `managedFields` and annotations where supported.
- **Independent candidate reconciliation.** Once this informer
  synchronizes, enqueue every candidate name once. Per-candidate
  queues coalesce repeated events, re-read the full CRD by name and
  retry failed GETs with independent backoff; one candidate's
  error or slow GET never prevents other candidates being processed.
  Only the supervisor's serialized `apply()` loop mutates runner,
  health and cleanup state. No periodic discovery polling.
- **Isolation.** One shared informer LIST/WATCH/sync failure is
  discovery-wide and reported under `CRDDiscovery` (A6); already
  running isolated watches and manager readiness remain unaffected.
  Missing events and informer errors never imply a CRD deletion.

### A4. Existing dynamic RESTMapper and `WatchStarted`

The pinned controller-runtime v0.24.0 default mapper
(`apiutil.NewDynamicRESTMapper`) already handles late-installed
kinds. `RESTMapping`, `KindFor`, `ResourceFor` and related
operations call `addKnownGroupAndReload` on NoMatch, which
re-fetches `/apis/<group>/<version>` and rebuilds the mapper.
This includes a newly served kind in a group/version already
mapped for another kind (e.g. DGD after DCD).

After full CRD readiness is confirmed, the supervisor calls
`RESTMapping(GK, version)` before starting a candidate through
the existing `apply()` path. If it returns NoMatch shortly after
`Established=True`, treat that as discovery lag: preserve the
previous CRD state and retry with backoff; mark Unhealthy only if
it persists past `CacheSyncTimeout` (2 minutes).

**No custom `ResettableRESTMapper` wrapper, no `MapperProvider`
change in `cmd/manager`, and no reset at transitions.** Existing
clients may cache a successful GVK mapping; A1 verifies the exact
compiled CRD name, kind, served version and namespaced scope so
that mapping remains correct for Ready. For Unservable or Removed,
stale mapping errors alone never authorize destructive cleanup.

`WatchStarted` (new, Normal) replaces `WatchRecovered` on the
first successful informer sync only for a kind installed or
recreated *after supervisor startup* (row 2). If its runner
stops before sync, neither event fires. Kinds already present at
startup, allowlist re-enables (row 10) and recovery (row 6)
retain their `WatchRecovered` semantics.

### A5. Leadership and concurrency

- `NeedLeaderElection()` stays true. The shared discovery informer, per-candidate
  reconciliation workers, runners, sweeps and fan-outs are created inside `Start`, so a standby reads no CRDs
  and sweeps nothing. Losing the lease ends `mgr.Start`.
- Per-candidate discovery workers submit results for serialized
  processing. Runners, health and sweeps change only in `apply()`,
  from one snapshot per pass; a blocked candidate GET cannot delay
  another candidate.
- A runner starts only if none is running and is awaited before
  removal, so duplicate runners cannot exist. Row 9's ordering keeps a
  sweep for an old UID from deleting a new generation's documents.
- Rapid flips collapse to the latest state; a CRD created and deleted
  between passes produces nothing.
- `AllowlistFanout.Trigger` today runs on `context.Background()` with a
  2-minute timeout behind a mutex. It gains `TriggerContext(ctx, …)`,
  which waits for its turn on a one-slot channel and gives up when
  `ctx` is done. The supervisor calls it from one coalescing goroutine
  under its root context and wait group. Supervised kinds without a
  running runner are skipped.
- On shutdown, cancelling the root context stops the shared informer,
  candidate workers and all other supervisor goroutines; `apply()`
  starts no sweep once it is cancelled; `wg.Wait()` covers all new
  goroutines (checked by the harness's goleak).

### A6. RBAC and discovery failures

One read-only, cluster-scoped rule, added as a `+kubebuilder:rbac`
marker, in `config/rbac/role.yaml`, and in the Helm ClusterRole with
this comment:

```yaml
# CRD discovery (Design 004 amendment, #226): one metadata-only CRD
# informer whose event handlers filter six candidate names; GET each
# candidate on change,
# to read Established and served versions. Read-only; never writes a CRD.
- apiGroups: ["apiextensions.k8s.io"]
  resources: ["customresourcedefinitions"]
  verbs: ["get", "list", "watch"]
```

CRDs are only read, so the `docs/external-crd-versions.md` invariant
holds. The single metadata informer records a LIST/WATCH/sync error as
a discovery-wide failure; each failed full-CRD GET is tracked by its
candidate name. One aggregate `CRDDiscovery` health entry exposes all
outstanding failures through Degraded status, one `WatchUnhealthy`
transition event and the gauge, clearing on recovery of the informer
and every failed candidate. Supervisor backoff retries failures.
A per-candidate GET failure freezes only that candidate's state;
a shared informer failure freezes discovery transitions for all
candidates. Neither error triggers a sweep, and existing runners,
the manager cache and readiness remain unaffected.

### A7. Testing

Envtest, with a harness option to omit chosen `config/crd/external/`
files at start and an event recorder:

- **Primary (Dynamo).** Start without
  `dynamographdeployments.nvidia.com`, with
  `dynamocomponentdeployments.nvidia.com` installed so the group-version
  is already mapped. Create an opted-in namespace and a vLLM Deployment
  and wait for its AIBOM. Install the DGD CRD, wait for `Established`
  and `v1beta1` served, then create a DGD. Expect, with no restart and
  no manager exit:
  - one `WatchStarted`, emitted after the informer synced, and no
    `WatchRecovered` for that start;
  - the DGD kind healthy, gauge 1, its AIBOM present, and no Degraded;
  - the Deployment's AIBOM intact, and a new Deployment inventoried.
- Both Dynamo CRDs absent at start; no external CRDs at start
  (supervisor added, no health entries).
- Removal: `WatchDisabled`, no `WatchUnhealthy`, AIBOM swept (envtest
  has no garbage collector), no Degraded. The LWS → StatefulSet fixture
  checks re-rooting.
- Allowlist: installed while excluded gives row 3; excluded and never
  installed gives no event, sweep or entry.
- Recreation with a new UID: `WatchDisabled` then `WatchStarted`.
- Served-version change on the two-version DGD fixture: `served: false`
  on `v1beta1` gives Degraded with AIBOMs kept; restoring it gives
  `WatchRecovered`.
- Duplicate events: no-op CRD updates (labels, `setDGDConversion`)
  cause no restart and no event.
- A forbidden or slow full-CRD GET for one candidate does not delay
  another candidate's late install. A shared informer LIST/WATCH
  failure reports `CRDDiscovery` while existing runners stay healthy.
- Existing watch-isolation, allowlist, roll-up and conversion suites,
  plus goleak, unchanged.
- Unit tests:
  - the A1 classification and A2 table;
  - `MarkDisabled` reasons;
  - one-informer name filtering and independent GET retries;
  - `TriggerContext` cancellation;
  - each `Watch().CRD` matching its fixture and the ClusterRole.

### A8. Rollout, two implementation PRs and open questions

Additive MINOR on the v1.7 train. Changes: one ClusterRole rule and
one Normal event reason. Unchanged: chart values, APIs, document shape
and metric names. CHANGELOG, `docs/external-crd-versions.md` and
Design 006's "CRD absent" row are updated with the implementation.

After the three-working-day design review window, implementation is
split into two separately reviewable PRs under this amendment:

1. **Phase A — late installation** (A2 rows 1–4 and 10):
   metadata-only discovery, startup/late watch activation,
   `workloadKinds` allowlist precedence, `WatchStarted`, read-only
   RBAC, the primary late-Dynamo envtest and allowlist cases.
   This first PR delivers the no-restart behavior. Full CRD
   deletion/unservability/recreation semantics are Phase B work.
2. **Phase B — lifecycle and cleanup** (A2 rows 5–9):
   Unservable, Removed and UID-change transitions,
   `MarkDisabled` reason in the `WatchDisabled` event message,
   stopping watches, sweeping stale AIBOMs, owner re-rooting,
   removal/recreation tests and cleanup regressions. This PR may
   follow Phase A by a week.

Open questions:
1. Is `CRDDiscovery` acceptable as a value of the gauge's `kind` label,
   or should it be a dedicated gauge?
2. Should a watched version that stops being served stay Degraded with
   AIBOMs kept (proposed), or be disabled and swept?
3. Should `resourceNames` narrowing to the six CRD names be a follow-up
   after checking all supported Kubernetes versions?
