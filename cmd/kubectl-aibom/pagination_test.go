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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestRun_AIBOMListPagination(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		path string
	}{
		{"summary all namespaces", []string{"summary", "-A"}, "/aiboms"},
		{"summary one namespace", []string{"summary"}, "/namespaces/prod/aiboms"},
		{"find all namespaces", []string{"find", "-A", "--runtime", "vllm"}, "/aiboms"},
		{"find one namespace", []string{"find", "--runtime", "vllm"}, "/namespaces/prod/aiboms"},
		{"expired continuation", []string{"summary", "-A"}, "/aiboms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const group = "/apis/aibom.k8saibom.dev/v1beta1"
			items := make([]unstructured.Unstructured, 1001)
			for i := range items {
				runtime := "vllm"
				if i%2 != 0 {
					runtime = "other"
				}
				name := fmt.Sprintf("aibom-%04d", i)
				if i == len(items)-1 {
					name += "-wider-than-any-name-in-previous-pages"
				}
				items[i].Object = map[string]any{
					"apiVersion": "aibom.k8saibom.dev/v1beta1",
					"kind":       "AIBOM",
					"metadata":   map[string]any{"namespace": "prod", "name": name},
					"status":     map[string]any{"summary": map[string]any{"runtime": map[string]any{"name": runtime}}},
				}
			}
			// Include an empty page with a continuation token. Kubernetes uses
			// the token, rather than the item count, to signal whether to stop.
			pages := [][]unstructured.Unstructured{items[:500], {}, items[500:1000], items[1000:]}
			tokens := []string{"", "cursor+1/=", "cursor+2/=", "cursor+3/=", ""}
			expired := tc.name == "expired continuation"
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(requests.Add(1)) - 1
				if r.Method != http.MethodGet || r.URL.Path != group+tc.path || i >= len(pages) {
					t.Errorf("unexpected request %d: %s %s", i+1, r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				q := r.URL.Query()
				if q.Get("limit") != "500" || q.Get("continue") != tokens[i] {
					t.Errorf("request %d query = %s, want limit=500 and continue=%q", i+1, q, tokens[i])
				}
				w.Header().Set("Content-Type", "application/json")
				if expired && i == 1 {
					w.WriteHeader(http.StatusGone)
					fmt.Fprint(w, "{\"apiVersion\":\"v1\",\"kind\":\"Status\",\"status\":\"Failure\",\"reason\":\"Expired\",\"code\":410}")
					return
				}
				page := &unstructured.UnstructuredList{Items: pages[i]}
				page.SetAPIVersion("aibom.k8saibom.dev/v1beta1")
				page.SetKind("AIBOMList")
				page.SetContinue(tokens[i+1])
				if err := json.NewEncoder(w).Encode(page); err != nil {
					t.Errorf("encode page: %v", err)
				}
			}))
			defer server.Close()

			cfg := clientcmdapi.NewConfig()
			cfg.Clusters["test"] = &clientcmdapi.Cluster{Server: server.URL}
			cfg.Contexts["test"] = &clientcmdapi.Context{Cluster: "test", Namespace: "prod"}
			cfg.CurrentContext = "test"
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			if err := clientcmd.WriteToFile(*cfg, kubeconfig); err != nil {
				t.Fatal(err)
			}

			stdout := captureOutput(t, &os.Stdout)
			stderr := captureOutput(t, &os.Stderr)
			err := run(append(tc.args, "--kubeconfig", kubeconfig))
			if expired {
				if !apierrors.IsResourceExpired(err) {
					t.Errorf("run error = %v, want expired continuation", err)
				}
				if requests.Load() != 2 || stdout() != "" || stderr() != "" {
					t.Error("expired continuation should end the listing without partial output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := int(requests.Load()); got != len(pages) {
				t.Errorf("got %d list requests, want %d", got, len(pages))
			}
			var wantOut, wantErr bytes.Buffer
			if tc.args[0] == "summary" {
				err = runSummary(testAIBOMItems(items...), nil, &wantOut, &wantErr)
			} else {
				err = runFind(testAIBOMItems(items...), findFilter{runtime: "vllm"}, &wantOut, &wantErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if stdout() != wantOut.String() || stderr() != wantErr.String() {
				t.Error("paginated output differs from rendering all items in one list")
			}
		})
	}
}

func testAIBOMItems(items ...unstructured.Unstructured) iter.Seq2[*unstructured.Unstructured, error] {
	return func(yield func(*unstructured.Unstructured, error) bool) {
		for i := range items {
			if !yield(&items[i], nil) {
				return
			}
		}
	}
}
