# Feeding BOMs to OWASP Dependency-Track

[Dependency-Track](https://dependencytrack.org/) is CycloneDX-native,
so k8s-aibom's ML-BOMs land there without format translation — each
tracked workload becomes a Dependency-Track project whose components
are the models, runtimes, and containers actually serving.

## The pattern

The controller's [webhook sink](../webhook-sink-protocol.md) POSTs the
raw CycloneDX JSON per BOM. Dependency-Track's ingestion endpoint
(`PUT /api/v1/bom`) wants an `X-Api-Key` header, a project identifier,
and the BOM base64-wrapped — so the integration is a small relay that
translates one shape into the other and decides the project mapping.
This is deliberate: the relay is where *your* project-naming policy
lives (per namespace, per workload, per team).

```
AIBOM controller ── webhook sink (HTTPS POST, raw BOM JSON)
        │
        ▼
   relay (this doc)  ──  PUT /api/v1/bom  ──►  Dependency-Track
   maps workload → DT project, wraps BOM, adds X-Api-Key
```

## Minimal relay

A complete, deployable example. The workload identity arrives inside
the BOM itself (`metadata.component` and the `k8s-aibom:workload:*`
properties), so the relay derives the project name from the document —
no extra plumbing.

```go
// relay.go: webhook sink -> Dependency-Track. Standard library only.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	dtURL := os.Getenv("DT_URL")      // e.g. https://dtrack.example.com
	apiKey := os.Getenv("DT_API_KEY") // BOM_UPLOAD + PROJECT_CREATION_UPLOAD
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		bom, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var doc struct {
			Metadata struct {
				Component struct {
					Name  string `json:"name"`
					Group string `json:"group"`
				} `json:"component"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(bom, &doc); err != nil || doc.Metadata.Component.Name == "" {
			http.Error(w, "not a k8s-aibom document", 400)
			return
		}
		project := doc.Metadata.Component.Group + "/" + doc.Metadata.Component.Name

		payload, _ := json.Marshal(map[string]any{
			"projectName":    project,
			"projectVersion": "runtime",
			"autoCreate":     true,
			"bom":            base64.StdEncoding.EncodeToString(bom),
		})
		req, _ := http.NewRequest(http.MethodPut, dtURL+"/api/v1/bom", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		// Pass Dependency-Track's answer back: on a 4xx the controller
		// puts it in the AIBOM's SinkFailed condition.
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		w.WriteHeader(resp.StatusCode)
		fmt.Fprintf(w, "forwarded %s -> %d %s", project, resp.StatusCode, answer)
	})
	_ = http.ListenAndServe(":8443", nil) // terminate TLS in front (Service mesh / ingress)
}
```

Deploy it wherever you run small services, then point the sink at it:

```yaml
# AIBOMControllerConfig excerpt (or chart config.sinks values)
sinks:
  - name: dependency-track
    type: Webhook
    webhook:
      endpoint: https://aibom-dt-relay.example.com/
```

The webhook sink retries on failure (bounded; see the
[protocol doc](../webhook-sink-protocol.md)) and a sink outage never
blocks BOM generation: `SinkFailed` surfaces on the AIBOM's
conditions and the CR-status copy remains authoritative.

**The relay cannot be a ClusterIP Service today.** The webhook sink
refuses to connect to private, loopback and link-local addresses (its
SSRF guard, see the [threat model](../threat-model.md)), and a
ClusterIP is a private address. Pointed at
`http://<relay>.<namespace>.svc.cluster.local/`, every upload fails
and the AIBOM's `SinkFailed` condition reads:

```
SSRF protection blocked connection to private/loopback/multicast IP: 10.96.22.13
```

The relay has to answer on an address that resolves to a public IP,
for example behind an ingress. Anyone who can reach that address can
then upload into Dependency-Track, so set `webhook.auth.bearerToken`
on the sink and check the `Authorization` header in the relay; the
example above does not.

**The Dependency-Track API key.** Create it on a team (the built-in
Automation team has no key by default in 4.14) and give the team
`BOM_UPLOAD` and `PROJECT_CREATION_UPLOAD`; the second is needed
because the relay sets `autoCreate`. With `BOM_UPLOAD` alone, the
first document of every new workload is refused with
`401 The principal does not have permission to create project.`
The relay passes that text back, so it shows up in `SinkFailed`.

**Documents that already exist are not sent.** The controller sends a
document when it builds one, and it skips the build while a workload's
inputs are unchanged. Adding the sink, or restarting a workload, sends
nothing for workloads that already have an AIBOM. Upload the current
documents once by hand:

```bash
kubectl get aibom -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' |
while read ns name; do
  kubectl aibom view "$name" -n "$ns" --raw |
    curl -sS -X POST https://aibom-dt-relay.example.com/ \
      -H 'Content-Type: application/vnd.cyclonedx+json; version=1.6' --data-binary @-
  echo
done
```

## Walkthrough

Tested with Dependency-Track 4.14.6 and a k8s-aibom controller built
from `main` at `cf8eeca`, on a kind cluster. Three Deployments ran
`ghcr.io/ggml-org/llama.cpp:server` with the model named in
`HF_MODEL_ID`: `team-a/chat` and `team-b/chat` with
`ggml-org/gemma-3-1b-it-GGUF`, and `team-a/summarizer` with
`Qwen/Qwen2.5-0.5B-Instruct-GGUF`.

The `team-a/chat` document in
[fixtures/llama-cpp-chat.cdx.json](fixtures/llama-cpp-chat.cdx.json)
was regenerated with a controller built from `main` at `110c434`,
after #364 added explicit dependencies. This regeneration replayed
the walkthrough's Deployment spec and kubelet-observed image digest
through an envtest Kubernetes API; it did not run the model or repeat
the consumer walkthrough. The fixture is byte for byte as
`kubectl aibom view --raw` printed it. Tests validate the CycloneDX
1.6 schema and check that the workload depends on every top-level
component.

You can post the fixture to a relay without a cluster:

```console
$ curl -sS -X POST http://localhost:8443/ \
    -H 'Content-Type: application/vnd.cyclonedx+json; version=1.6' \
    --data-binary @docs/integrations/fixtures/llama-cpp-chat.cdx.json
forwarded team-a/chat -> 200 {"token":"<upload id>"}
```

Project `team-a/chat`, version `runtime`, gets three components
(`GET /api/v1/component/project/{uuid}`):

| Classifier | Name | Version | Package URL |
|---|---|---|---|
| `MACHINE_LEARNING_MODEL` | `ggml-org/gemma-3-1b-it-GGUF` | | |
| `CONTAINER` | `ghcr.io/ggml-org/llama.cpp` | `server` | `pkg:oci/llama.cpp@sha256%3Aca40…5390?repository_url=ghcr.io%2Fggml-org%2Fllama.cpp&tag=server` |
| `APPLICATION` | `llama.cpp` | | |

Every `k8s-aibom:` property is kept on its component, split at the
first colon into a group and a name
(`GET /api/v1/component/{uuid}/property`):

```
MACHINE_LEARNING_MODEL ggml-org/gemma-3-1b-it-GGUF
  k8s-aibom confidence = inferred
  k8s-aibom container:name = server
  k8s-aibom evidence:locator = spec.template.spec.containers[0].env[0](HF_MODEL_ID)
  k8s-aibom evidence:source = env_var
  k8s-aibom identity:confidence = claimed
  k8s-aibom identity:envVarName = HF_MODEL_ID
```

The purl is what ties workloads together. All three workloads run the
same image digest, and the identity lookup returns all three projects
(the key needs `VIEW_PORTFOLIO`):

```console
$ curl -sG "$DT/api/v1/component/identity" -H "X-Api-Key: $KEY" \
    --data-urlencode 'purl=pkg:oci/llama.cpp@sha256:ca40726778c25a7fadce42c09bada8484eab22e28bc283bdbf212e87b1555390?repository_url=ghcr.io%2Fggml-org%2Fllama.cpp&tag=server' |
  jq -r '.[].project.name' | sort
team-a/chat
team-a/summarizer
team-b/chat
```

Dependency-Track stores the purl with the colon after `sha256`
encoded, as in the table; the lookup matches either spelling, and
`purl=pkg:oci/llama.cpp` without version or qualifiers finds the same
three components. `GET /api/v1/component/hash/<digest>` finds them by
the digest alone. Models have no purl; look them up by name
(`identity?name=ggml-org/gemma-3-1b-it-GGUF` returned `team-a/chat`
and `team-b/chat`).

## What you get in Dependency-Track

- One auto-created project per workload (`namespace/name`), version
  `runtime`. Each upload replaces the project's component list: a
  model that left the workload is deleted from the project, and
  Dependency-Track keeps no copy of the earlier list, so you see the
  current state, not a diff.
- Models as `MACHINE_LEARNING_MODEL` components and images as
  `CONTAINER` components, with every `k8s-aibom:` property on its
  component (group `k8s-aibom`) for audit queries through the API.
- Cross-project questions answered by purl or digest, such as which
  workloads run this image.
- Policies on component coordinates. This condition flags every
  workload that serves a Gemma model; it raised a violation on
  `team-a/chat` and `team-b/chat` and none on `team-a/summarizer`:

  ```json
  {"subject": "COORDINATES", "operator": "MATCHES",
   "value": "{\"name\": \"ggml-org/gemma-*\"}"}
  ```

  Leave `group` and `version` out of the value. Model components have
  neither, and Dependency-Track counts a pattern on a missing field,
  even `*`, as no match.

## Known limitations

Found in the walkthrough above with Dependency-Track 4.14.6. The
document is not changed to suit a consumer; these are recorded so you
know what to expect.

- **Policies cannot read properties or the classifier.** In 4.14 the
  policy subjects are coordinates, purl, CPE, SWID tag ID, version,
  version distance, hash, license, license group, age, the internal
  flag and vulnerability fields. A policy on
  `k8s-aibom:security:signing:status` or `k8s-aibom:confidence`, or one
  that applies only to `MACHINE_LEARNING_MODEL` components, cannot be
  written; an allow-list written as `COORDINATES` `NO_MATCH` also
  fires on the runtime and container components. Read those
  properties through the API instead.
- **Document metadata is not shown.** The project gets no group,
  properties or metadata from `metadata.component`, and
  `metadata.properties`, `metadata.lifecycles` and `metadata.tools`
  do not appear on it. The workload's identity survives only in the
  project name the relay chooses.
- **Two components with the same name and type become one.** When a
  model is found twice, for example in a container argument and in an
  environment variable, the document has two
  `machine-learning-model` components with the same name.
  Dependency-Track keeps one, with the properties of the first.
- **Only a one-level dependency graph.** Documents built from `main`
  after #364 include a `dependencies` array linking the workload
  (`metadata.component.bom-ref`) to every top-level component. There
  are no edges between components, so the graph does not describe
  which runtime uses which model or the packages inside an image.
  The original `cf8eeca` walkthrough predated this array; its empty
  dependency graph is no longer a limitation of current documents.
- **No vulnerability findings from the document alone.** The internal
  analyzer checks the one component with a purl, the `pkg:oci`
  container, and found nothing for it. Models and runtimes have no
  purl or CPE to match.

## Notes

- Version note: `k8s-aibom:security:signing:*` properties (including
  `verified`) require k8s-aibom v1.5.0+ with `spec.verification`
  enabled; everything else works on any release. Before v1.7.0 they
  were named `signature.*`.
- The relay is intentionally yours to own and extend (project ACL
  mapping, dedup, environment tagging). If a native Dependency-Track
  sink would serve you better than this pattern, say so on the issue
  tracker. Sinks are demand-gated, and that request is the demand.
