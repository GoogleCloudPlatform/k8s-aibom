# Building from Source

Most users should install from the published, attested release artifacts —
see the [Quickstart](../README.md#quickstart). Build from source when you
are working in an air-gapped environment, maintaining a fork, or developing
the controller itself.

## Build and push your own image

```bash
git clone https://github.com/GoogleCloudPlatform/k8s-aibom.git
cd k8s-aibom

export IMG=my-registry.example.com/k8s-aibom:dev

# Cross-compiles linux/amd64 + linux/arm64
make image-multiarch
make docker-push
```

> [!WARNING]
> **Platform architecture:** plain `make image` builds only your host's
> native architecture — an Apple Silicon build will CrashLoop on an AMD64
> cluster with `exec format error`. Prefer `make image-multiarch`.

The build stamps a version into the binary and image labels; pass
`VERSION=<value>` to `make` to override the default (`dev`).

## Deploy the local chart

```bash
helm install k8s-aibom ./charts/k8s-aibom \
  --namespace k8s-aibom-system \
  --create-namespace \
  --set image.repository=my-registry.example.com/k8s-aibom \
  --set image.tag=dev
```

No `helm` installed? `make helm` bootstraps one at `./bin/helm`.

> [!NOTE]
> **Private registries:** if your registry requires authentication, add
> `--set imagePullSecrets[0].name=my-secret` to the Helm command.

## Air-gapped notes

- The published release assets (image, chart, `install.yaml`, SBOM) carry
  Sigstore attestations that verify offline once mirrored; mirroring the
  official image by digest preserves its verifiability, which a local
  rebuild does not.
- If you rebuild rather than mirror, your artifacts carry your provenance,
  not the project's — sign them under your own identity.

## Development

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the development workflow and
testing discipline (`make test`, envtest, the conservative-detection
conventions).
