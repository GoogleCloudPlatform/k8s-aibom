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
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRun_ArgumentErrorsNeedNoCluster(t *testing.T) {
	withoutCluster(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"bogus"}, `unknown subcommand "bogus"`},
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
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "no configuration has been provided") {
			t.Errorf("run(%q) = %v, want the kubeconfig error", args, err)
		}
	}
}
