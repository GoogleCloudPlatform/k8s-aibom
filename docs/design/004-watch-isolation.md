# Design 004: Watch isolation for third-party kinds

Status: Draft, 2026-10-02. Fixes #127. Targets the v1.6 train; on the
critical path for the AICR Dynamo-pairing qualification. Review window
stated on the PR; the Dynamo and AICR reviewers already on #127 are
specifically invited.

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
  starts (today: restart required; unchanged, now documented).
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
