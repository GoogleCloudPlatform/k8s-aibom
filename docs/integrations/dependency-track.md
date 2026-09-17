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
the BOM itself (`metadata.component` and the `aibom.workload.*`
properties), so the relay derives the project name from the document —
no extra plumbing.

```go
// relay.go — webhook-sink → Dependency-Track. ~50 lines, no deps.
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
	dtURL := os.Getenv("DT_URL")        // e.g. https://dtrack.example.com
	apiKey := os.Getenv("DT_API_KEY")   // DT automation key (BOM_UPLOAD)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
		_ = json.Unmarshal(bom, &doc)
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
		w.WriteHeader(resp.StatusCode)
		fmt.Fprintf(w, "forwarded %s -> %d", project, resp.StatusCode)
	})
	_ = http.ListenAndServe(":8443", nil) // terminate TLS in front (Service mesh / ingress)
}
```

Deploy it wherever you run small services (a Deployment next to
Dependency-Track is typical), then point the sink at it:

```yaml
# AIBOMControllerConfig excerpt (or chart config.sinks values)
sinks:
  - name: dependency-track
    type: Webhook
    webhook:
      endpoint: https://aibom-dt-relay.dtrack.svc.example.com/
```

The webhook sink retries on failure (bounded; see the
[protocol doc](../webhook-sink-protocol.md)) and a sink outage never
blocks BOM generation — `SinkFailed` surfaces on the AIBOM's
conditions and the CR-status copy remains authoritative.

## What you get in Dependency-Track

- One auto-created project per workload (`namespace/name`), refreshed
  on every BOM change — drift shows up as component diffs.
- Models as `machine-learning-model` components with the
  `aibom.confidence` / `signature.*` properties preserved for policy
  and audit queries.
- Dependency-Track's own policy engine on top: notify on new
  unmanaged models, on `signature.status` ≠ `verified` in designated
  projects, or on any component change in regulated namespaces —
  judgments live in your consumer, facts come from the cluster.

## Notes

- Version note: `signature.*` properties (including `verified`)
  require k8s-aibom v1.5.0+ with `spec.verification` enabled;
  everything else works on any release.
- The relay is intentionally yours to own and extend (project ACL
  mapping, dedup, environment tagging). If a native Dependency-Track
  sink would serve you better than this pattern, say so on the issue
  tracker — sinks are demand-gated, and that request is the demand.
