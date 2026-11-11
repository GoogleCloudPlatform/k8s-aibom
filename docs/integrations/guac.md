# Loading BOMs into GUAC

[GUAC](https://guac.sh/) reads CycloneDX documents as they are and
puts their packages, artifacts and relations in one graph. Loaded
next to other SBOMs, a k8s-aibom document answers questions such as
which workloads run this image digest, and which ones serve a given
model.

Tested with GUAC v1.1.0 (the release's demo compose file, in-memory
backend) and documents from a k8s-aibom controller built from `main`
at `cf8eeca`, on the same kind cluster as the
[Dependency-Track walkthrough](dependency-track.md#walkthrough):
`team-a/chat` and `team-b/chat` serving `ggml-org/gemma-3-1b-it-GGUF`,
`team-a/summarizer` serving `Qwen/Qwen2.5-0.5B-Instruct-GGUF`, all
three on the same `ghcr.io/ggml-org/llama.cpp:server` digest. The
`team-a/chat` document is
[fixtures/llama-cpp-chat.cdx.json](fixtures/llama-cpp-chat.cdx.json),
now regenerated with the `main` controller at `110c434` and explicit
workload-to-component dependencies. See the
[fixture provenance](dependency-track.md#walkthrough) for the API-input
replay used to regenerate it. The GUAC results below are from the
original `cf8eeca` run, not a repeat with the regenerated fixture.

## Getting documents into GUAC

GUAC collects documents; it does not take an HTTP POST from the
webhook sink. Write the documents to a place a collector reads, and
run the collector. The walkthrough used `guacone collect files` on a
directory:

```bash
curl -sLO https://github.com/guacsec/guac/releases/download/v1.1.0/guac-demo-compose.yaml
docker compose -f guac-demo-compose.yaml -p guac up -d

mkdir -p boms
kubectl get aibom -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' |
while read ns name; do
  kubectl aibom view "$name" -n "$ns" --raw > "boms/$ns-$name.cdx.json"
done

docker run --rm --network guac_default -v "$PWD/boms:/boms:ro" \
  ghcr.io/guacsec/guac:v1.1.0 /opt/guac/guacone collect files /boms \
  --gql-addr=http://graphql:8080/query --csub-addr=collectsub:2782
```

The release has no `guacone` binary for darwin/arm64, so the commands
run it from the GUAC image. To try it without a cluster, mount
`docs/integrations/fixtures` instead of `boms`.

For continuous loading, a small receiver in front of the webhook sink
can write each document to a bucket that one of GUAC's blob or file
collectors watches. That path was not part of this walkthrough.

## What GUAC builds

For the three documents GUAC v1.1.0 created these packages:

```
pkg:guac/cdx/chat
pkg:guac/cdx/summarizer
pkg:oci/ghcr.io/ggml-org/llama.cpp@sha256:ca40726778c2…?tag=server
pkg:guac/pkg/llama.cpp
pkg:guac/pkg/ggml-org/gemma-3-1b-it-GGUF
pkg:guac/pkg/Qwen/Qwen2.5-0.5B-Instruct-GGUF
```

- Each document becomes a `HasSBOM` on `pkg:guac/cdx/<metadata.component.name>`.
- Each component becomes an `IsDependency` of that package.
- The container keeps its `pkg:oci` purl, and an `IsOccurrence` ties it
  to the artifact `sha-256:ca40726778c2…`.
- The runtime and the models have no purl, so GUAC gives them
  `pkg:guac/pkg/<name>` purls.

## Queries

`guacone query known package` takes the purl from the document and
finds the package and its occurrence, the image digest:

```console
$ docker run --rm --network guac_default ghcr.io/guacsec/guac:v1.1.0 \
    /opt/guac/guacone query known package \
    'pkg:oci/llama.cpp@sha256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390?repository_url=ghcr.io%2Fggml-org%2Fllama.cpp&tag=server' \
    --gql-addr=http://graphql:8080/query
...
| occurrence | 19        | Occurrence for Artifact: sha-256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390 |
| occurrence | 29        | Occurrence for Artifact: sha-256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390 |
| occurrence | 34        | Occurrence for Artifact: sha-256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390 |
```

One occurrence per document. Which workloads run the image is a
reverse lookup through GraphQL: every package that depends on it.

```console
$ curl -s localhost:8080/query -H 'Content-Type: application/json' -d '{"query":
    "{ IsDependency(isDependencySpec:{dependencyPackage:{type:\"oci\",name:\"llama.cpp\"}}) { package { namespaces { names { name } } } } }"}' |
  jq -r '.data.IsDependency[].package.namespaces[].names[].name' | sort
chat
chat
summarizer
```

With `{type:"guac",name:"gemma-3-1b-it-GGUF"}` the same query returned
`chat` twice. Both are `pkg:guac/cdx/chat`; see the first limitation
below.

## Linking a workload to its image's SBOM

An SBOM of the image, scanned by the digest in the document's purl,
holds the packages inside the container. GUAC does not link it to the
k8s-aibom document on its own. The walkthrough used Syft v1.33.0:

```bash
docker run --rm anchore/syft:v1.33.0 \
  ghcr.io/ggml-org/llama.cpp@sha256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390 \
  --platform linux/arm64 -o cyclonedx-json > boms/llama-cpp-image.cdx.json
```

Syft records the image as `metadata.component` with the digest as its
version. GUAC turns that into the artifact `sha256:ca40…`, while the
k8s-aibom container's `hashes` (`"alg": "SHA-256"`) become the
artifact `sha-256:ca40…`. Same digest, two artifact nodes, and no edge
between them. A `HashEqual` joins them:

```graphql
mutation {
  ingestHashEqual(
    artifact: {artifactInput: {algorithm: "sha-256", digest: "ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390"}}
    otherArtifact: {artifactInput: {algorithm: "sha256", digest: "ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390"}}
    hashEqual: {justification: "same OCI digest", origin: "manual", collector: "manual", documentRef: "manual"}
  )
}
```

Saved as `mutation.graphql`, it can be sent with
`jq -Rs '{query: .}' mutation.graphql | curl -s localhost:8080/query -H 'Content-Type: application/json' -d @-`.
After that, GraphQL `path` from `pkg:guac/cdx/chat` to a Debian
package from the image SBOM (`pkg:deb/ubuntu/adduser`) returned:

```
Package -> HasSBOM -> Artifact (sha-256) -> HashEqual -> Artifact (sha256) -> HasSBOM -> Package
```

Before the mutation, the only path between them ran through the shared
`guac` package type node, which links nothing real.

The digest in the document is the one the kubelet reports, here the
digest of a multi-arch image index. Scan by that digest, as above,
with `--platform` set to your nodes' platform, so the image SBOM
records the same value.

## Known limitations

Found with GUAC v1.1.0. The document is not changed to suit a
consumer; these are recorded so you know what to expect.

- **Same-name workloads in different namespaces merge.** The top-level
  package is `pkg:guac/cdx/<metadata.component.name>`; the namespace
  in `metadata.component.group` is dropped. `team-a/chat` and
  `team-b/chat` become one package with two `HasSBOM` entries, told
  apart only by document digest.
- **Properties are dropped.** No `HasMetadata` is created; confidence,
  evidence, signing status and workload identity are not in the graph.
- **Component types are lost.** Models and runtimes become plain
  `pkg:guac/pkg/` packages; nothing marks them as
  `machine-learning-model` or `application`. A model found twice in
  one document is one package.
- **Only workload-level dependencies.** Current documents explicitly
  link the workload to every top-level component, but carry no edges
  between components. They do not say which runtime uses which model
  or describe the packages inside an image. The original `cf8eeca`
  documents had no `dependencies` array, so GUAC connected every
  component to the top-level package with type `UNKNOWN` and the
  justification `top-level package GUAC heuristic connecting to each
  file/package`. That absence is no longer a limitation of current
  documents.
- **The OCI purl is reshaped.** `repository_url=ghcr.io/ggml-org/llama.cpp`
  becomes the namespace `ghcr.io/ggml-org`, and only the `tag`
  qualifier is kept.
- **The image SBOM is not linked by default.** See
  [Linking a workload to its image's SBOM](#linking-a-workload-to-its-images-sbom).
- **No vulnerability data for models.** The OSV certifier
  (`guacone certifier osv`) checked only the `pkg:oci` package and
  recorded `novuln`; it skipped the `pkg:guac/pkg/` packages.
