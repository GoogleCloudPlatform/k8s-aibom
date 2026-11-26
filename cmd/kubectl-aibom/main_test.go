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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// withoutCluster points the kubeconfig loading rules at a file that
// does not exist and hides any in-cluster configuration, so a run that
// reaches the kubeconfig fails with "no configuration has been
// provided". It also discards the usage text run prints to stderr.
func withoutCluster(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = devNull
	t.Cleanup(func() {
		os.Stderr = stderr
		_ = devNull.Close()
	})
}

func TestRun_HelpNeedsNoCluster(t *testing.T) {
	withoutCluster(t)
	for _, args := range [][]string{
		{"help"},
		{"-h"},
		{"--help"},
		{"summary", "-h"},
		{"find", "--help"},
		{"view", "-h", "my-aibom"},
		{"verify", "--help"},
	} {
		if err := run(args); err != nil {
			t.Errorf("run(%q) = %v, want nil", args, err)
		}
	}
}

func TestRun_VersionNeedsNoCluster(t *testing.T) {
	withoutCluster(t)
	stdout := captureOutput(t, &os.Stdout)
	if err := run([]string{"--version"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout()); got != "dev" {
		t.Errorf("--version = %q, want dev", got)
	}
}

func TestRun_SummaryHintUsesNamespaceOptIn(t *testing.T) {
	const generalHint = "No AIBOMs found. Nothing is inventoried until a namespace is opted in:\n" +
		"  kubectl label namespace <name> aibom.k8saibom.dev/enabled=true\n" +
		"The controller reports the same in the NamespacesOptedIn condition of AIBOMControllerConfig/default.\n"
	for _, tc := range []struct {
		name          string
		allNamespaces bool
		items         string
		labels        string
		wantHint      string
		wantRequests  int32
	}{
		{
			name:   "opted in",
			labels: `{"aibom.k8saibom.dev/enabled":"true"}`,
			wantHint: "No AIBOMs found. Namespace prod-jobs is opted in.\n" +
				"Check that the controller is running. Then inspect status.conditions and status.effectiveInferenceRuntimeImagePatterns with:\n" +
				"  kubectl get aibomcontrollerconfig default -o yaml\n",
			wantRequests: 2,
		},
		{
			name:         "not opted in",
			labels:       `{}`,
			wantHint:     generalHint,
			wantRequests: 2,
		},
		{
			name:         "explicitly opted out",
			labels:       `{"aibom.k8saibom.dev/enabled":"false"}`,
			wantHint:     generalHint,
			wantRequests: 2,
		},
		{
			name:         "namespace lookup forbidden",
			wantHint:     generalHint,
			wantRequests: 2,
		},
		{
			name:         "populated namespace",
			items:        `[{"apiVersion":"aibom.k8saibom.dev/v1beta1","kind":"AIBOM","metadata":{"name":"server","namespace":"prod-jobs"}}]`,
			wantRequests: 1,
		},
		{
			name:          "empty across all namespaces",
			allNamespaces: true,
			wantHint:      generalHint,
			wantRequests:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listPath := "/apis/aibom.k8saibom.dev/v1beta1/namespaces/prod-jobs/aiboms"
			if tc.allNamespaces {
				listPath = "/apis/aibom.k8saibom.dev/v1beta1/aiboms"
			}
			items := tc.items
			if items == "" {
				items = "[]"
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case listPath:
					fmt.Fprintf(w, `{"apiVersion":"aibom.k8saibom.dev/v1beta1","kind":"AIBOMList","items":%s}`, items)
				case "/api/v1/namespaces/prod-jobs":
					if tc.labels == "" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
						return
					}
					fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"prod-jobs","labels":%s}}`, tc.labels)
				default:
					t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			cfg := clientcmdapi.NewConfig()
			cfg.Clusters["test"] = &clientcmdapi.Cluster{Server: server.URL}
			cfg.Contexts["test"] = &clientcmdapi.Context{Cluster: "test", Namespace: "prod-jobs"}
			cfg.CurrentContext = "test"
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			if err := clientcmd.WriteToFile(*cfg, kubeconfig); err != nil {
				t.Fatal(err)
			}

			stdout := captureOutput(t, &os.Stdout)
			stderr := captureOutput(t, &os.Stderr)
			args := []string{"summary", "--kubeconfig", kubeconfig}
			if tc.allNamespaces {
				args = append(args, "-A")
			}
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != tc.wantRequests {
				t.Errorf("API requests = %d, want %d", got, tc.wantRequests)
			}
			if hint := stderr(); hint != tc.wantHint {
				t.Errorf("stderr = %q, want %q", hint, tc.wantHint)
			}
			if got := stdout(); !strings.HasPrefix(got, "NAMESPACE") || strings.Contains(got, "No AIBOMs") {
				t.Errorf("stdout = %q, want only the summary table", got)
			}
		})
	}
}

func captureOutput(t *testing.T, stream **os.File) func() string {
	t.Helper()
	output, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	original := *stream
	*stream = output
	t.Cleanup(func() {
		*stream = original
		_ = output.Close()
	})
	return func() string {
		t.Helper()
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestRun_ArgumentErrorsNeedNoCluster(t *testing.T) {
	withoutCluster(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"bogus"}, `unknown subcommand "bogus"`},
		{[]string{"--version", "extra"}, "--version does not take arguments"},
		{[]string{"find", "--signed", "maybe"}, "--signed must be one of"},
		{[]string{"find", "--digest", "sha256:"}, "--digest"},
		{[]string{"view"}, "view requires exactly one AIBOM name"},
		{[]string{"verify", "a", "b"}, "verify requires exactly one AIBOM name"},
		// An argument the subcommand does not use is an error, not
		// silently dropped: `find vllm` used to list every AIBOM, and
		// `view NAME -A` to send a cluster-scoped GET that 404s.
		{[]string{"find", "vllm"}, `find takes no positional arguments, got "vllm"`},
		{[]string{"find", "--runtime", "vllm", "extra"}, `find takes no positional arguments, got "extra"`},
		{[]string{"summary", "prod-jobs"}, `summary takes no positional arguments, got "prod-jobs"`},
		{[]string{"summary", "--runtime", "vllm"}, "--runtime does not apply to summary"},
		{[]string{"summary", "--raw"}, "--raw does not apply to summary"},
		{[]string{"view", "my-aibom", "-A"}, "-A does not apply to view"},
		{[]string{"verify", "-A", "my-aibom"}, "-A does not apply to verify"},
		{[]string{"verify", "my-aibom", "--raw"}, "--raw does not apply to verify"},
		{[]string{"view", "my-aibom", "--model", "Qwen"}, "--model does not apply to view"},
		// -n and -A together: -A used to win and -n was dropped, so
		// the command listed every namespace.
		{[]string{"find", "-A", "-n", "prod-jobs", "--runtime", "vllm"}, "-n and -A cannot be used together (-n prod-jobs for one namespace"},
		{[]string{"find", "-n=prod-jobs", "-A"}, "-n and -A cannot be used together"},
		{[]string{"summary", "-n", "prod-jobs", "-A"}, "-n and -A cannot be used together"},
		{[]string{"summary", "-A", "-n", ""}, "-n and -A cannot be used together (-n NAMESPACE for one namespace"},
		// A flag the subcommand does not take is still reported first.
		{[]string{"view", "my-aibom", "-n", "prod-jobs", "-A"}, "-A does not apply to view"},
		// --A is -A: it takes no value, so the token after it is
		// checked on its own. It used to be swallowed as --A's value,
		// and `summary --A prod-jobs` listed every namespace.
		{[]string{"summary", "--A", "prod-jobs"}, `summary takes no positional arguments, got "prod-jobs"`},
		{[]string{"find", "--A", "vllm"}, `find takes no positional arguments, got "vllm"`},
		{[]string{"find", "--A", "-n", "prod-jobs"}, "-n and -A cannot be used together (-n prod-jobs for one namespace"},
		{[]string{"view", "--A", "my-aibom"}, "-A does not apply to view"},
		// kubectl's long spellings follow the same rules as -n and -A.
		{[]string{"find", "--all-namespaces", "--namespace", "prod-jobs"}, "-n and -A cannot be used together (-n prod-jobs for one namespace"},
		{[]string{"summary", "-A", "--namespace=prod-jobs"}, "-n and -A cannot be used together (-n prod-jobs for one namespace"},
		{[]string{"summary", "--all-namespaces", "prod-jobs"}, `summary takes no positional arguments, got "prod-jobs"`},
		{[]string{"view", "my-aibom", "--all-namespaces"}, "--all-namespaces does not apply to view"},
		// After "--" every token is positional, flag-shaped or not.
		{[]string{"find", "--", "vllm"}, `find takes no positional arguments, got "vllm"`},
		{[]string{"view", "--"}, "view requires exactly one AIBOM name"},
		{[]string{"view", "a", "--", "b"}, "view requires exactly one AIBOM name"},
		{[]string{"verify", "--", "-n", "prod-jobs"}, "verify requires exactly one AIBOM name"},
	} {
		err := run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

// A find filter given an empty value (an unset shell variable,
// `--digest "$DIGEST"`) is an error. It used to count as no filter, so
// the command listed every AIBOM as a match.
func TestRun_FindEmptyFilterIsAnError(t *testing.T) {
	withoutCluster(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"find", "-A", "--runtime", ""}, "--runtime needs a value"},
		{[]string{"find", "-A", "--model="}, "--model needs a value"},
		{[]string{"find", "-A", "--image", ""}, "--image needs a value"},
		{[]string{"find", "-A", "--digest", ""}, "--digest needs a value"},
		{[]string{"find", "-A", "--signed="}, "--signed needs a value"},
		{[]string{"find", "--runtime", "vllm", "--digest", ""}, "--digest needs a value"},
	} {
		err := run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

// -n, --namespace, --context or --kubeconfig given an empty value (an
// unset shell variable, `verify "$NAME" -n "$NS"`) is an error. Each
// used to count as not given, so the command quietly read the
// kubeconfig's namespace, context or file instead.
func TestRun_EmptyScopeFlagIsAnError(t *testing.T) {
	withoutCluster(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"summary", "-n", ""}, "-n needs a value"},
		{[]string{"find", "--runtime", "vllm", "-n="}, "-n needs a value"},
		{[]string{"view", "my-aibom", "--namespace", ""}, "--namespace needs a value"},
		{[]string{"verify", "--namespace=", "my-aibom"}, "--namespace needs a value"},
		{[]string{"summary", "-A", "--context", ""}, "--context needs a value"},
		{[]string{"view", "my-aibom", "--kubeconfig="}, "--kubeconfig needs a value"},
		{[]string{"verify", "my-aibom", "-n", "", "--context", ""}, "--context, -n needs a value"},
	} {
		err := run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

// A well-formed command still reaches the kubeconfig; without one it
// fails there, as before.
func TestRun_ValidCommandStillLoadsKubeconfig(t *testing.T) {
	withoutCluster(t)
	for _, args := range [][]string{
		{"summary"},
		{"summary", "-A"},
		{"find", "-A", "--runtime", "vllm", "--signed", "verified"},
		{"find", "-A=false", "-n", "prod-jobs"},
		{"find", "-n", "prod-jobs", "--model=Qwen", "--image", "vllm", "--digest", "sha256:abc"},
		{"view", "my-aibom", "-n", "prod-jobs", "--raw"},
		// Both spellings of each boolean flag take no value.
		{"summary", "--A"},
		{"find", "--A", "--runtime", "vllm"},
		{"find", "--A", "--signed", "verified", "--model", "Qwen"},
		{"view", "-raw", "my-aibom", "-n", "prod-jobs"},
		{"view", "--raw", "my-aibom"},
		{"verify", "-n", "prod-jobs", "my-aibom"},
		// kubectl's long spellings of -n and -A.
		{"summary", "--namespace", "prod-jobs"},
		{"summary", "--all-namespaces"},
		{"find", "--all-namespaces", "--runtime", "vllm"},
		{"find", "--namespace=prod-jobs", "--model", "Qwen"},
		{"view", "my-aibom", "--namespace", "prod-jobs", "--raw"},
		{"verify", "--namespace", "prod-jobs", "my-aibom"},
		// "--" ends the flags; the name after it is the positional
		// argument. It used to be swallowed as the value of "--".
		{"view", "--", "my-aibom"},
		{"view", "-n", "prod-jobs", "--raw", "--", "my-aibom"},
		{"verify", "--", "my-aibom"},
		{"summary", "-n", "prod-jobs", "--"},
		{"find", "-A", "--runtime", "vllm", "--"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "no configuration has been provided") {
			t.Errorf("run(%q) = %v, want the kubeconfig error", args, err)
		}
	}
}
