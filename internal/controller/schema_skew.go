/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/openapi"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// Schema skew: the API server prunes stored fields that the SERVED CRD
// schema does not declare. If a cluster's AIBOMControllerConfig CRD is
// replaced by an older schema than the running controller (GitOps sync
// pinned to an older revision, a CreateReplace rollback, a manual CRD
// re-apply), fields such as spec.verification vanish from the stored
// object with no error — and a controller reading the CR alone cannot
// tell "operator left it unset" from "the API server discarded it". A
// verification control silently OFF behind Ready=True is the one shape
// this project must not have (#104).
//
// The controller therefore reads the served schema through the OpenAPI
// v3 endpoint (reachable by every authenticated ServiceAccount via
// system:discovery; no CRD read permission required) and compares it
// against every top-level spec field the binary was built with. Any
// field the binary knows and the server does not is reported as skew:
// Degraded=True, a Warning Event, and a log line. The check is generic
// over the spec struct so the next additive field cannot reintroduce
// the failure.

const (
	openAPIConfigGroupVersionPath = "apis/aibom.k8saibom.dev/v1beta1"
	openAPIConfigSchemaName       = "dev.k8saibom.aibom.v1beta1.AIBOMControllerConfig"
)

// SchemaChecker reports the top-level AIBOMControllerConfig spec fields
// this controller was built with that the API server's served schema
// does not declare. An empty result means no skew.
type SchemaChecker interface {
	MissingSpecFields(ctx context.Context) ([]string, error)
}

// OpenAPISchemaChecker implements SchemaChecker against the cluster's
// OpenAPI v3 document.
type OpenAPISchemaChecker struct {
	Client openapi.Client
}

// NewOpenAPISchemaChecker wraps an OpenAPI v3 client (typically
// discovery.NewDiscoveryClientForConfig(cfg).OpenAPIV3()).
func NewOpenAPISchemaChecker(c openapi.Client) *OpenAPISchemaChecker {
	return &OpenAPISchemaChecker{Client: c}
}

// MissingSpecFields fetches the served v1beta1 schema and diffs it
// against the compiled-in spec. Errors mean "could not determine"
// (endpoint unavailable, group not yet published) and callers must
// treat them as unknown, never as skew and never as a reconcile failure.
func (c *OpenAPISchemaChecker) MissingSpecFields(_ context.Context) ([]string, error) {
	paths, err := c.Client.Paths()
	if err != nil {
		return nil, fmt.Errorf("openapi v3 paths: %w", err)
	}
	gv, ok := paths[openAPIConfigGroupVersionPath]
	if !ok {
		return nil, fmt.Errorf("openapi v3: %q not published", openAPIConfigGroupVersionPath)
	}
	doc, err := gv.Schema(runtime.ContentTypeJSON)
	if err != nil {
		return nil, fmt.Errorf("openapi v3 schema for %q: %w", openAPIConfigGroupVersionPath, err)
	}
	served, err := servedSpecFieldsFromOpenAPI(doc)
	if err != nil {
		return nil, err
	}
	return missingFields(expectedSpecFields(), served), nil
}

// expectedSpecFields lists the JSON names of every top-level field on
// AIBOMControllerConfigSpec as compiled into this binary.
func expectedSpecFields() []string {
	t := reflect.TypeOf(aibomv1beta1.AIBOMControllerConfigSpec{})
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// servedSpecFieldsFromOpenAPI extracts the declared top-level spec
// property names for AIBOMControllerConfig from an OpenAPI v3 group-
// version document.
func servedSpecFieldsFromOpenAPI(doc []byte) (map[string]struct{}, error) {
	var parsed struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, fmt.Errorf("openapi v3: parse: %w", err)
	}
	schema, ok := parsed.Components.Schemas[openAPIConfigSchemaName]
	if !ok {
		return nil, fmt.Errorf("openapi v3: schema %q not found", openAPIConfigSchemaName)
	}
	spec, ok := schema.Properties["spec"]
	if !ok {
		return nil, fmt.Errorf("openapi v3: schema %q has no spec", openAPIConfigSchemaName)
	}
	out := make(map[string]struct{}, len(spec.Properties))
	for k := range spec.Properties {
		out[k] = struct{}{}
	}
	return out, nil
}

// missingFields returns, sorted, the expected fields absent from served.
func missingFields(expected []string, served map[string]struct{}) []string {
	var missing []string
	for _, f := range expected {
		if _, ok := served[f]; !ok {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	return missing
}

// schemaSkewMessage is the operator-facing text for Degraded and the
// Warning Event. It names the fields, states the consequence plainly,
// and carries the remedy.
func schemaSkewMessage(missing []string) string {
	return fmt.Sprintf(
		"The served AIBOMControllerConfig CRD schema predates this controller: spec field(s) [%s] are not declared, "+
			"so any stored values for them were pruned by the API server and the corresponding features are OFF. "+
			"If the k8s-aibom-crds chart manages these CRDs, upgrade it to this controller's version; "+
			"otherwise apply the matching CRDs directly "+
			"(helm show crds oci://ghcr.io/googlecloudplatform/charts/k8s-aibom --version <chart-version> | "+
			"kubectl apply --server-side --force-conflicts --field-manager=k8s-aibom-crds -f -), "+
			"then re-apply your configuration and restart the controller.",
		strings.Join(missing, ", "),
	)
}
