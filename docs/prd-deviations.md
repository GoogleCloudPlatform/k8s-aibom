# PRD deviations

This document records places where the implementation deliberately deviates
from the literal text of the PRD. Each entry describes the PRD position, the
implementation position, and the rationale. The next PRD revision should
absorb any deviation here that has stabilized.

This document is distinct from
[`docs/schema-divergences.md`](schema-divergences.md): that file tracks
CycloneDX 1.6 ML-BOM schema gaps and Trusera-comparison choices. This file
tracks "PRD said X, code does Y" implementation decisions.

## Format for deviation entries

```
### P-NNN: <short title>

**Status:** active | rescinded | absorbed-into-PRD
**Introduced in:** <phase or commit>
**PRD section:** <§ reference>

**What the PRD says**

<quote or paraphrase>

**What the code does**

<concrete description>

**Why**

<rationale>

**PRD revision proposal**

<the text the next PRD revision should adopt, or "no change needed">
```

## Active deviations

### P-001: Workload.Spec replaced with Workload.Object (client.Object)

**Status:** active
**Introduced in:** Phase 4
**PRD section:** §8.3 (Scraper interface canonical Go signature)

**What the PRD says**

```go
type Workload struct {
    ...
    Spec       map[string]any  // typed per kind in concrete impls
    Pods       []corev1.Pod
}
```

**What the code does**

```go
type Workload struct {
    ...
    Object    client.Object   // typed; can be *appsv1.Deployment, *unstructured.Unstructured, etc.
    Pods      []corev1.Pod
}
```

`Object` is `sigs.k8s.io/controller-runtime/pkg/client.Object`, which is the
union of `metav1.Object` and `runtime.Object`. It exposes
`GetName`/`GetNamespace`/`GetAnnotations`/`GetLabels`/etc. for free and is
directly type-assertable to the concrete kind (`*appsv1.Deployment`,
`*unstructured.Unstructured` for arbitrary CRDs).

**Why**

`map[string]any` would force every scraper to re-implement K8s metadata
lookup and would lose all compile-time type safety. The PRD's trailing
comment ("typed per kind in concrete impls") signaled the literal type was
illustrative, not load-bearing. `client.Object` is the controller-runtime
standard abstraction and what every existing K8s controller uses.

**PRD revision proposal**

Replace the §8.3 type sketch with:

```go
type Workload struct {
    Kind      WorkloadKind
    Category  WorkloadCategory
    Namespace string
    Name      string
    UID       types.UID
    Object    client.Object  // sigs.k8s.io/controller-runtime/pkg/client.Object
    Pods      []corev1.Pod
}
```
