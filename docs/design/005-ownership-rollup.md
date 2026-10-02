# Design 005: Ownership roll-up for CRD-owned workloads (+ CronJob, pod digests for CRD kinds)

Status: Draft, 2026-10-02 (implementation note: a Pod source maps pod
create/delete/digest events to the owning root through the chain, so
the owner's document follows its descendants' pods without the
children re-triggering it). Implements Design 003 §3; tracks #126.
Targets the v1.6 train; on the critical path for the AICR
Dynamo-pairing qualification (without it, every Dynamo graph reports
one AIBOM for the graph plus one per component Deployment). Review
window stated on the PR. Depends on Design 004 (the supervisor is
where the new `DynamoComponentDeployment` watch lives).

## Context

v1.6 added three CRD kinds — `DynamoGraphDeployment`, `NIMService`,
`LeaderWorkerSet` — each reported as one AIBOM keyed to the CR. The
operators behind them materialize ordinary workloads the controller
*also* watches:

| Root | Materializes (observed) |
|---|---|
| `DynamoGraphDeployment` | `DynamoComponentDeployment` per component → `Deployment` \| `LeaderWorkerSet` \| Grove `PodCliqueSet`/`PodClique` (multi-node) → Pods |
| `NIMService` | `Deployment` \| `LeaderWorkerSet` → Pods |
| `LeaderWorkerSet` | `StatefulSet` per group → Pods |
| `CronJob` (not watched today) | `Job` → Pods |

Today those children get their own AIBOMs: a one-worker Dynamo graph
produces two documents saying the same thing, and an auditor filtering
by digest sees every image twice. Separately, the CRD kinds resolve
digests only from digest-pinned image references because no pods are
listed at the CR level — the operator's pods are two or three
ownership hops away.

## Goal

One workload, one AIBOM: a tracked workload owned — directly or
transitively via controller `ownerReferences` — by another *tracked*
kind is not separately reported; the owner's AIBOM is the report, and
it says what it absorbed. Pod-status digests reach the CRD kinds
through the same ownership walk. CronJob coverage lands on the same
machinery.

## Non-goals

- Label- or selector-based attribution. Ownership only (the
  pod_ownership.go rule): the Dynamo operator stamps
  `nvidia.com/dynamo-graph-deployment-name` on pods, but a tenant pod
  can carry that label too; ownerReferences cannot be forged across
  objects the tenant does not control.
- Stub AIBOMs for suppressed workloads (Design 003 open question 2:
  properties on the owner's document; additive if ever needed).
- Changing how roots without tracked owners are reported.

## Decision

### 1. Suppression, kind-neutral, in `reconcileWorkload`

After the namespace opt-in check and before scraping, the reconciler
resolves the workload's **tracked owner**:

- Walk controller `ownerReferences` upward from the workload object,
  one live `Get` per hop using the reference's own `apiVersion`/`kind`/
  `name` (unstructured; no typed dependency), depth ≤ 6.
- Stop with **tracked owner found** when an ancestor's kind is in the
  tracked-kind set: the kinds this process registered — apps/v1
  `Deployment`/`StatefulSet`/`DaemonSet`, batch `Job`/`CronJob`, and
  every third-party kind handed to the supervisor at startup (present
  CRDs, regardless of current watch health, so suppression is stable
  through an outage).
- Stop with **no tracked owner** at a root (no controller owner), or
  when a hop fails with NotFound, Forbidden or NoMatch: an intermediate
  kind we cannot read is treated as an untracked owner and the
  workload is **reported as today**. Coverage never regresses because
  of a missing permission; the miss is counted and logged once per
  kind.
- Workloads with no controller owner at all — the overwhelming majority
  of Deployments — pay nothing: zero extra requests.

A suppressed workload: no scrape, no AIBOM; any existing AIBOM for it
is deleted (the owner's document supersedes it, exactly as the
not-opted-in and unmatched branches already do); outcome counter
`aibom_workload_reconcile_outcomes_total{outcome="rolled_up"}`.

### 2. The owner's document says what it absorbed, and gets the pods

Root kinds (`DynamoGraphDeployment`, `NIMService`, `LeaderWorkerSet`,
`CronJob`) resolve their **descendants** before scraping:

- Build a UID → controller-owner-UID map for the namespace from
  cache-backed typed lists (`Deployment`, `ReplicaSet`, `StatefulSet`,
  `Job`) plus live unstructured lists of the intermediate CRD kinds
  that are present (`DynamoComponentDeployment`; Grove `PodCliqueSet`,
  `PodClique`, `PodCliqueScalingGroup`). Descendants are the closure
  under that map from the root UID.
- **Recorded**: each descendant that is a tracked kind becomes a
  metadata property `aibom.rollup.owned.<i>` = `<Kind>/<name>` (one
  indexed property per descendant, sorted by kind then name;
  deterministic; same indexing convention as `aibom.scrape.<i>`). Nothing silently
  disappears: the suppressed identities are on the owner's document.
- **Pods**: pods in the namespace whose controller-owner chain reaches
  the root UID are passed as `Workload.Pods`, so the existing
  `resolveContainerDigest` (container name + image match against pod
  status) resolves digests for CRD kinds exactly as it does for a
  Deployment via its ReplicaSets. Grove's pods are reached through
  `PodClique` ownership; no label is consulted.

Cost: namespace-scoped cache lists for the apps kinds (already paid by
the Deployment path), plus one live list per present intermediate CRD
kind per root reconcile — bounded by the number of roots and their
reconcile rate (input-hash fast path unchanged).

### 3. CronJob

A `CronJobReconciler` on the manager cache (apps path; typed), one
AIBOM per CronJob from `spec.jobTemplate`. The eval scraper already
accepts `*batchv1.CronJob`. Jobs it spawns roll up under §1; their pods
reach the CronJob's document under §2. RBAC: `cronjobs` get/list/watch.

### 4. Standalone `DynamoComponentDeployment`

A DCD with no controller owner is its own root (Design 003 §4): it
joins the supervisor as a fifth third-party kind, scraped by the
Dynamo scraper applied to the shared component spec as a one-component
graph. DCDs owned by a graph are suppressed under §1. RBAC:
`dynamocomponentdeployments` get/list/watch.

### 5. RBAC added, all read-only

| Group | Resources | Verbs | Why |
|---|---|---|---|
| `batch` | `cronjobs` | get, list, watch | §3 |
| `nvidia.com` | `dynamocomponentdeployments` | get, list, watch | §2 walk, §4 root |
| `grove.io` | `podcliquesets`, `podcliques`, `podcliquescalinggroups` | get, list | §2 walk only (never watched); absent CRD or denied permission degrades to "report as today" |

## Degradation

| Situation | Result |
|---|---|
| Intermediate kind unreadable (RBAC, CRD absent) | child reported as today; `rollup_unresolved` counted; one log per kind |
| Root kind's watch degraded (Design 004) | suppression unchanged (tracked = registered); root's existing AIBOM kept |
| Owner deleted, children linger | children's next reconcile finds no tracked owner → reported as roots |
| Walk exceeds depth 6 | treated as untracked; reported as today |

## Testing

- Unit: chain resolution against a fake client — tracked owner at hop
  1 and hop 3; untracked root; Forbidden/NotFound/NoMatch mid-walk;
  depth cap; descendants closure and pod attribution through
  `ReplicaSet` and through an unstructured intermediate.
- Envtest: `LeaderWorkerSet` → `StatefulSet` (StatefulSet produces no
  AIBOM, its prior AIBOM is deleted, the LWS document lists it and
  resolves a digest from the StatefulSet's pod status);
  `DynamoGraphDeployment` → `DynamoComponentDeployment` → `Deployment`
  (one document, two `aibom.rollup.owned` entries); `CronJob` → `Job`;
  untracked owner (a StatefulSet owned by a kind with no RBAC) still
  reported; standalone DCD root. Byte-identity for roots with no
  descendants: unchanged documents.

## Rollout

Additive, v1.6 train. New documents gain `aibom.rollup.owned`
properties only when they absorb something; a Deployment with no
tracked owner produces byte-identical output. CHANGELOG notes that
child AIBOMs disappear on upgrade for the four chains above.

## Open questions

1. Should `aibom.rollup.owned` carry the child's UID as well as
   `Kind/name` (stable across recreate vs. readable)? Proposal: name
   only in the property, UID in a sibling `aibom.rollup.owned.uid`
   only if a consumer asks.
2. Grove `PodGangMap` and `ClusterTopologyBinding` are not in the walk;
   they do not own pods. Confirm with the Dynamo/Grove reviewers.
