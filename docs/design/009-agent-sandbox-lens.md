# Design 009: agent-sandbox lens — Agent Sandbox and OpenShell `Sandbox` coverage

Status: Draft, 2026-11-25. Tracks #330; targets the v1.8 train. Review
window: one week from the opening of the PR (GOVERNANCE.md). Review
from Agent Sandbox and OpenShell maintainers, and from anyone running
either on GKE or OpenShift AI, is invited, in particular on the unit of
inventory (§1, open question 1), where this note departs from the shape
sketched in #330.

## Context

#330 names three runtimes that express their agent execution units as
the same Kubernetes object: Agent Sandbox on GKE, NVIDIA OpenShell's
Kubernetes driver, and Red Hat's packaging of OpenShell in OpenDataHub
and OpenShift AI. None of them produces an AIBOM today: `Sandbox` is not
in `KnownWorkloadKinds` (Design 006), no scraper handles it, and
`agent.spec` reads only the apps/v1 kinds.

What the upstream sources say, read at the pinned releases
(kubernetes-sigs/agent-sandbox v1.0.12, 2026-11-20;
ai-openshell/openshell v0.1.10, 2026-11-24, formerly NVIDIA/OpenShell):

- **`Sandbox`** is `agents.x-k8s.io/v1beta1`, namespaced. Agent Sandbox
  v1.0.x serves only v1beta1; v0.5.x served v1beta1 and v1alpha1;
  v0.4.x and earlier served only v1alpha1. **`SandboxTemplate`,
  `SandboxClaim` and `SandboxWarmPool`** are in a separate group,
  `extensions.agents.x-k8s.io/v1beta1`. #330 lists `SandboxTemplate`
  and `SandboxClaim` next to `Sandbox` under `agents.x-k8s.io/v1beta1`;
  they are served from the extensions group.
- `Sandbox.spec` inlines the blueprint it shares with `SandboxTemplate`:
  `spec.podTemplate.metadata` (labels and annotations only) and
  `spec.podTemplate.spec` (a `PodSpec`). The Sandbox controller creates
  one Pod per Sandbox and is that Pod's controller owner.
- **Ownership.** A Sandbox created for a `SandboxClaim`, or adopted by
  one from a warm pool, is controller-owned by the claim. An unclaimed
  Sandbox pre-provisioned by a `SandboxWarmPool` (one per replica of the
  pool) is controller-owned by the pool. A Sandbox created directly,
  which is what OpenShell does, has no owner.
- **A `SandboxTemplate` owns no Sandbox and no Pod** (it controls only
  the NetworkPolicy it manages). Its blueprint is copied into the
  Sandbox at creation. The Sandbox names it in the annotation
  `agents.x-k8s.io/sandbox-template-ref`, and a pool names it in
  `spec.sandboxTemplateRef.name`.
- `SandboxClaim.spec` carries `warmPoolRef`, `lifecycle`,
  `additionalPodMetadata`, `volumeClaimTemplates` and `env`, a list of
  names *and values*. It has no pod template. A claim with `env` is
  always created cold, and when the template's
  `envVarsInjectionPolicy` allows it, the claim controller copies those
  entries, values included, into the Sandbox's `spec.podTemplate.spec`
  containers.
- OpenShell's Kubernetes driver creates `Sandbox` objects directly,
  preferring v1beta1 and falling back to v1alpha1 for older Agent
  Sandbox controllers. Agent Sandbox must be installed before OpenShell.
  The driver labels each Sandbox `openshell.ai/managed-by: openshell`,
  `openshell.ai/sandbox-id`, `openshell.ai/sandbox-name`,
  `openshell.ai/sandbox-workspace` and `openshell.ai/gateway-id`. Per
  the driver's README, its supervisor Pod carries a *non-controller*
  owner reference to the Sandbox.

## Goal

Every `Sandbox` in an opted-in namespace is reported as one AIBOM,
category `agent`, with its container images and, from its rolled-up
Pod, their digests. The claim, pool or template it came from, and the
three OpenShell labels #330 names, are recorded as facts. Installing
Agent Sandbox after k8s-aibom needs no restart.

## Non-goals

- Reading OpenShell's gateway state or policy, or any control plane
  behind a sandbox (#330 "Not in scope"). Inventory records the sandbox
  and what runs in it.
- **Agent Substrate.** Per #330 its actors are not Kubernetes objects.
  This is recorded as a limitation. A `WorkerPool` was not investigated
  for this note and is not reported; per #330 it can be added later as
  infrastructure if a defensible fact to attach is found.
- `agents.x-k8s.io/v1alpha1` Sandboxes. Clusters on Agent Sandbox v0.4.x
  or earlier serve only v1alpha1 and are not covered; OpenShell's
  fallback to it is not followed.
- The OpenShell supervisor Pod. Owned by the Sandbox through a
  non-controller reference, it is not rolled up under Design 005 and its
  image is not recorded. It is OpenShell's control path, not the agent.
- Judging isolation (runtime class, network policy, seccomp). These are
  facts a policy engine can read from the Pod; this lens does not grade
  them.
- Recording any env value, from the pod template or from a claim.

## Decision

### 1. `Sandbox` is the unit of inventory

Only `Sandbox` becomes a tracked kind. #330 proposes the
`SandboxTemplate` or `SandboxClaim` as the unit when one exists; the
upstream ownership model does not support either as a root:

- A `SandboxTemplate` owns no Sandbox, so Design 005 has nothing to roll
  up into it. Templates are shared: one template backs many sandboxes,
  each running its own Pod, and one document per template would merge
  them.
- A `SandboxClaim` has no pod template. A claim-rooted document would
  have to read the owned Sandbox's spec across an ownership edge, which
  no scraper does today; Design 005 hands a root its descendants' Pods,
  not their specs.
- The Sandbox carries the full pod template and controls the Pod in all
  three paths (claim, warm pool, direct), so one kind covers Agent
  Sandbox and OpenShell alike.
- A warm-pool Sandbox keeps its name and UID when a claim adopts it;
  its controller owner, labels and `spec.podTemplate.metadata` change.
  A per-Sandbox document therefore keeps its identity across adoption
  and is rebuilt once at adoption.
- Each idle replica of a warm pool is its own Sandbox with its own Pod,
  so a pool of N replicas yields N documents while they sit idle (open
  question 2).

The AIBOM is named by `AIBOMNameForWorkload("agents.x-k8s.io",
"Sandbox", name)`, as for every other kind.

### 2. Facts

| Source | Becomes |
|---|---|
| `spec.podTemplate.spec` containers and init containers | container Components with image references, through the shared inference extraction (`extractContainerComponentAt`); locators rooted at `spec.podTemplate.spec` |
| the rolled-up Pod's status | digests on those container Components, as for LeaderWorkerSet, DisaggregatedSet and Dynamo |
| `spec.podTemplate.spec` | the `agent.spec` extraction (agent images, framework and model-provider env-name signatures), same locator root |
| the kind itself | runtime application Component `agent-sandbox`, **declared**, source `crd_field`, locator `kind` (the NIMService precedent); it makes the category `agent` |
| `metadata.ownerReferences` (controller) | `k8s-aibom:sandbox:owner` = `<Kind>/<name>` as written (`SandboxClaim`, `SandboxWarmPool` or any other controller); absent for a direct Sandbox |
| annotation `agents.x-k8s.io/sandbox-template-ref` | `k8s-aibom:sandbox:template` = template name |
| labels `openshell.ai/managed-by`, `openshell.ai/gateway-id`, `openshell.ai/sandbox-id` | `k8s-aibom:sandbox:openshell:managedBy`, `…:gatewayId`, `…:sandboxId`, each only when present |

- The declared component is what produces a document for a Sandbox
  with no agent signal: the reconciler skips a workload whose scrape is
  unresolved. The category `agent` therefore comes from the operator's
  choice of kind, not from a detection signal. That is a departure from
  "Conservative detection" in CONTRIBUTING.md, taken because #330
  states that a sandbox is an agent execution environment; Agent
  Sandbox itself also ships reinforcement-learning sandboxes (the
  `agent-sandbox-gymnasium` RLlib example), which would be labelled
  `agent` too. Open question 4 puts the alternative.
- Component order: the `agent.spec` components come before the declared
  `agent-sandbox` component, so the status summary's first runtime is a
  detected framework when there is one, and `agent-sandbox` otherwise.
  The declared name says what was chosen, never which agent runs.
- The `k8s-aibom:sandbox` properties go on the `agent-sandbox`
  component, as the `k8s-aibom:nim` properties go on NIM's. The
  sub-namespace and names are proposed here and are added to
  `docs/cyclonedx-property-taxonomy.md` in the implementation PR.
- Labels are recorded as written. `managed-by: openshell` says which
  driver created the object; it is never read as a statement about
  policy, isolation or the gateway. `sandbox-name` and
  `sandbox-workspace` are not recorded: #330 does not ask for them, and
  reviewers who want them should say so.
- Implementation: a `Sandbox` scraper that maps `spec.podTemplate` onto
  a `PodTemplateSpec` (labels, annotations, `PodSpec`) and runs two
  existing extractions over it with `spec.podTemplate.spec` as the
  locator root: inference.spec's `extractContainerComponentAt` with the
  rolled-up Pods, for images and digests, and `agent.spec`'s
  pod-template extraction, which gains a spec-locator parameter as
  `training.spec` and `eval.spec` already have. It does not run
  inference.spec's env and arg model extraction, which records
  allowlisted env values as model names (§5). No new detection logic.

### 3. Watch, late install and allowlist

- `Sandbox` runs under the Design 004 supervisor, pinned to
  `agents.x-k8s.io/v1beta1`. `sandboxes.agents.x-k8s.io` becomes the
  seventh workload-kind candidate of the #226 discovery and the eighth
  CRD it filters, after Design 007's auxiliary `InferencePool`; this
  extends the "six candidates" of the accepted Design 004 amendment
  (A1, A3), and the implementation updates the candidate count in the
  Helm ClusterRole comment. Installing Agent Sandbox after k8s-aibom
  (OpenShell requires it first) starts the watch without a restart;
  removal, recovery and re-creation follow the #226 transitions.
- `agents.x-k8s.io/Sandbox` joins `KnownWorkloadKinds` (Design 006), so
  `spec.discovery.workloadKinds` can include or exclude it, and
  excluding it tears the watch down like any third-party kind.
- `SandboxClaim`, `SandboxWarmPool` and `SandboxTemplate` are not
  tracked and not watched.

### 4. RBAC, read-only

| API group | Resource | Verbs |
|---|---|---|
| `agents.x-k8s.io` | `sandboxes` | `get`, `list`, `watch` |

Added to `config/rbac/role.yaml` and the Helm ClusterRole, with the
same upgrade note shape as #226. No rule is added for
`extensions.agents.x-k8s.io`. The ownership walk
(`resolveTrackedOwner`) issues a GET for every controller owner,
tracked or not, so each reconcile of a claimed or pooled Sandbox sends
one GET that the shipped RBAC denies. The walk then ends as unresolved:
the Sandbox is still reported as a root, as the walk guarantees, and
the reconcile is counted as `rollup_unresolved`. The owner fact comes
from the Sandbox's own owner reference and does not need the fetch.
Open question 3 weighs this against two more `get` rules.

### 5. Secrets and credentials

No env value is recorded. `agent.spec` records env var *names*
(`SourceEnvVarNamePresent`), and a `valueFrom` only as present, never
resolved; the Sandbox scraper does not run the env and arg model
extraction. This matters more here than for a Deployment: a claim's env
values can be copied as literals into the Sandbox's pod template, so
they are in the object the lens reads. They must never reach a
document, a status, a log line or a sink. A cluster whose own RBAC lets
k8s-aibom read `SandboxClaim` objects puts claim env values in the
controller's memory through the ownership walk; nothing from them is
recorded either.

### Fixture

A minimal test-only CRD under `config/crd/external/` for
`sandboxes.agents.x-k8s.io` (v1beta1), per the rule against vendoring
upstream CRDs verbatim. Test objects are built from the Agent Sandbox
v1.0.12 examples and the OpenShell v0.1.10 driver's labels and owner
references, trimmed to the fields read. Corpus cases (#314) come from
both projects' samples.

## Degradation

| Situation | Result |
|---|---|
| Sandbox CRD absent | kind not registered (log), as every third-party kind; a later install starts the watch (#226) |
| Sandbox CRD present, v1beta1 not served (Agent Sandbox ≤ v0.4) | not a usable candidate under #226; no Sandbox AIBOMs |
| Sandbox watch unhealthy | Design 004: Degraded for `Sandbox` only; existing AIBOMs kept |
| claimed or pooled Sandbox | reported as a root; one denied owner GET per reconcile, counted `rollup_unresolved`; owner property from the Sandbox's own reference |
| Sandbox suspended (Pod terminated) | document kept; container digests unresolved while no Pod exists |
| pod template fails to decode | recorded on the document's errors, no components from it (the Dynamo precedent) |

## Testing

- Unit: locators rooted at `spec.podTemplate.spec`; container
  Components for containers and init containers; the declared
  `agent-sandbox` component and category `agent` with and without an
  agent image match (a signal-free Sandbox still gets a document);
  component order and the summary's runtime; each property present only
  when its source is; no env value, literal or claim-injected, appears
  in the output; the apps/v1 `agent.spec` locators are unchanged;
  byte-determinism.
- Envtest, claim path: a `Sandbox` whose controller owner is a claim
  and which controls a Pod produces one AIBOM, category `agent`, the
  digest from the Pod's status on the container Component,
  `k8s-aibom:sandbox:owner` = `SandboxClaim/<name>`,
  `k8s-aibom:sandbox:template` from the annotation, and the
  `rollup_unresolved` count for the denied owner read.
- Envtest, warm pool: a pool-owned Sandbox is reported with the pool as
  owner; after the owner reference moves to a claim, the same AIBOM
  records the claim and no second document appears.
- Envtest, OpenShell shape: a direct Sandbox with the `openshell.ai/*`
  labels records them as properties; a supervisor-shaped Pod with a
  non-controller owner reference is not rolled up.
- Envtest, degradation: the Sandbox CRD installed after startup starts
  the watch; a CRD serving only v1alpha1 starts none; excluding the kind
  in `spec.discovery.workloadKinds` tears the watch down; a suspended
  Sandbox without a Pod keeps its document with digests unresolved; a
  malformed `spec.podTemplate` is recorded as an error and the
  reconcile does not fail.
- Live, as #330 asks: the GKE Agent Sandbox quickstart and an OpenShell
  Kubernetes-driver deployment on the permitted cluster, recorded as an
  evidence note under `docs/evidence/`. The namespaces OpenShell
  renders sandboxes into (one shared namespace by default, one per
  workspace in its managed and operator modes) need the opt-in like
  any other namespace; the evidence note states how they were opted
  in.

## Rollout

Additive MINOR on the v1.8 train (`docs/roadmap.md`): one new tracked
kind, one read-only RBAC rule with an upgrade note, and new properties
documented in the taxonomy and in `docs/external-crd-versions.md`. No
change to the `AIBOM` API. An empty `spec.discovery.workloadKinds`
means every known kind, so on upgrade `Sandbox` is reported without a
configuration change wherever the CRD exists and the namespace is opted
in; the CHANGELOG says so. One implementation PR after the review
window closes.

## Open questions

1. **Unit of inventory.** #330 proposes the `SandboxTemplate` or
   `SandboxClaim`; this note proposes the `Sandbox` for the reasons in
   §1. A claim-rooted document is possible but needs a new path that
   reads an owned object's spec; reviewers who prefer it should say so.
2. **Unclaimed warm-pool sandboxes.** They run the agent image in a
   live Pod, so this note reports them, one document per idle replica,
   with the pool as owner. Each is a root, so each costs a reconcile
   with a namespace-wide descendant listing, an `AIBOM` object and a
   sink emission; Agent Sandbox's own sizing comments mention pools of
   about 1–2k replicas. This cost is not measured here. Suppressing
   idle sandboxes until claimed would hide running Pods; reviewers who
   run large pools should say which they prefer. The cost above counts
   documents, not reconciles: every further reconcile of a replica
   repeats the namespace-wide listing and the denied owner GET (§4)
   and, today, writes status. #426 (an open PR fixing #409) would cap
   those writes at one every five minutes while only the timestamp
   changes; a replica whose scrape reports extraction errors still
   writes on every reconcile, because its error counter changes.
3. **Denied owner reads for claimed and pooled sandboxes.** Adding
   `get` on `sandboxclaims` and `sandboxwarmpools`
   (`extensions.agents.x-k8s.io`) would let the walk finish cleanly and
   end the denied GET per reconcile, at the cost of two rules and of
   fetching claim objects, which can carry env values. This note leaves
   them out.
4. **Category from the kind.** Agent Sandbox describes itself as
   managing "isolated, stateful, singleton workloads, ideal for use
   cases like AI agent runtimes and reinforcement learning". This note
   follows #330 and reports every Sandbox as `agent`. The alternative
   follows Conservative detection: category `agent` only with an agent
   signal, and no document for a signal-free Sandbox, which then also
   loses its image and digest inventory.
