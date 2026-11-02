# k8s-aibom Terraform module

Deploys the k8s-aibom controller to a GKE cluster. **By default this
installs the published Helm chart** from
`oci://ghcr.io/googlecloudplatform/charts/k8s-aibom` at a pinned
version — the controller image is digest-pinned by the chart, and
every release ships provenance and SBOM attestations. The default
path needs no Artifact Registry, no Cloud Build, and incurs no build
billing.

## Quickstart (published artifact — recommended)

```hcl
module "k8s_aibom" {
  source           = "github.com/GoogleCloudPlatform/k8s-aibom//terraform"
  project_id       = "my-project"
  cluster_name     = "my-cluster"
  cluster_location = "us-central1-c"
  # chart_version = "1.5.0"   # optional; defaults to the current release
}
```

Then:

```bash
terraform init
terraform apply
```

Verify what was installed against the release attestations — see the
supply-chain section of the [main README](../README.md).

## Building from source (air-gap / development)

Set `build_from_source = true` to compile the controller from this
checkout via Cloud Build, push to a project-local Artifact Registry
repository, and deploy the local chart with the image overridden.
This path deploys an **unsigned, locally built image on a mutable
tag** — it exists for air-gapped mirrors and development, not as the
standard install. See
[docs/building-from-source.md](../docs/building-from-source.md).

```hcl
module "k8s_aibom" {
  source            = "github.com/GoogleCloudPlatform/k8s-aibom//terraform"
  project_id        = "my-project"
  cluster_name      = "my-cluster"
  cluster_location  = "us-central1-c"
  build_from_source = true
  image_tag         = "dev-abc123"   # required on this path
  # repository_id   = "aibom-repo"   # Artifact Registry repo to create
}
```

> [!WARNING]
> The source path requires the `cloudbuild.googleapis.com` and
> `artifactregistry.googleapis.com` APIs and incurs standard Cloud
> Build / Artifact Registry billing. The default path requires
> neither.

## Prerequisites

- Terraform >= 1.0
- A running GKE cluster and credentials to reach it
- Source path only: Google Cloud SDK (`gcloud`) installed and
  authenticated

## Variables

| Name | Default | Purpose |
|---|---|---|
| `project_id` | — | GCP project |
| `cluster_name` | — | Target GKE cluster name |
| `cluster_location` | `us-central1-c` | Target GKE cluster's region or zone |
| `namespace` | `k8s-aibom-system` | Install namespace |
| `chart_version` | current release | Published chart version (default path) |
| `build_from_source` | `false` | Opt into the source path |
| `image_tag` | `""` | Tag for the locally built image (source path, required) |
| `repository_id` | `aibom-repo` | Artifact Registry repo (source path) |
| `region` | `us-central1` | Provider region / AR location |
