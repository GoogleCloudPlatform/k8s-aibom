# Migrating to the `v1beta1` API

The graduation release serves both `aibom.k8saibom.dev/v1alpha1` and
`v1beta1` (schema-identical; conversion strategy `None`) with **storage
on `v1beta1`**. Design and rationale:
[Design 001](design/001-api-graduation-v1beta1.md).

## What you must do at upgrade (everyone)

**Apply the new CRDs before (or with) the chart upgrade.** Helm,
Helmfile, and Flux (default `Skip`) never update existing CRDs on a
chart upgrade; only Argo CD does:

```bash
helm show crds oci://ghcr.io/googlecloudplatform/charts/k8s-aibom --version <new> \
  | kubectl apply --server-side --field-manager=k8s-aibom-crds -f -
```

Server-side apply is required — the CRDs exceed the client-side
annotation size limit.

**If you skip this step, the rollout stalls loudly — and safely:** the
graduated controller's informers request `v1beta1`, which the old CRD
does not serve; cache start fails, the new pod never reports Ready, and
the rolling update **stalls with the previous pod still running and
serving** — inventory continues uninterrupted on the old version while
the Deployment surfaces `ProgressDeadlineExceeded` and the new pod logs
`no matches for kind ... v1beta1` explicitly. Applying the CRDs lets
the rollout complete. (Verified on a real GKE cluster as part of the
release-candidate boundary testing.)

## What happens to your existing objects (nothing you must do)

- Existing objects stored as `v1alpha1` remain fully readable through
  both API versions — the schemas are identical and conversion is
  `None`. There is no data risk at upgrade.
- New and updated writes are stored as `v1beta1` automatically.
- AIBOMs rewrite themselves organically: the controller updates their
  status on reconcile, so the inventory converges to `v1beta1` storage
  within normal reconcile cycles.
- The `AIBOMControllerConfig` rewrites on its next update; a no-op
  touch works:

```bash
kubectl annotate aibomcontrollerconfig default aibom.k8saibom.dev/storage-touch- --overwrite
kubectl annotate aibomcontrollerconfig default aibom.k8saibom.dev/storage-touch=migrated
kubectl annotate aibomcontrollerconfig default aibom.k8saibom.dev/storage-touch-
```

## Clients and manifests (no action required)

`v1alpha1` clients, manifests, and GitOps pipelines keep working
unchanged for all of 1.x. Adopt `v1beta1` in your manifests at your own
pace; the fields are identical.

## The eventual removal of `v1alpha1` (future, announced)

Removing `v1alpha1` is a **separate, announced release** — it will not
happen within 1.x. When announced, it will gate on:

1. All stored objects rewritten to `v1beta1` (see above — usually
   automatic), and
2. `v1alpha1` removed from each CRD's `status.storedVersions`:

```bash
kubectl patch crd aiboms.aibom.k8saibom.dev \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1beta1"]}}'
kubectl patch crd aibomcontrollerconfigs.aibom.k8saibom.dev \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1beta1"]}}'
```

Run the patches only after confirming rewrites are complete (all
AIBOMs have reconciled since the upgrade and the config has been
touched). Downstream distributions (e.g. NVIDIA AICR) assert
`spec.versions[?storage].name == 'v1beta1'` at graduation and treat
`storedVersions` cleanup as part of the later removal gate — matching
this document's split.
