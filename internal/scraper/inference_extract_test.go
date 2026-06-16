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

package scraper

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// testFullDigest is the canonical 64-char hex sha256 used throughout
// scraper tests for fixtures where digest extraction is expected to
// succeed. Anything shorter than this MUST be rejected by the strict
// validation in cleanSHA256Digest.
const testFullDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testFullDigestHex is testFullDigest with the "sha256:" prefix stripped,
// for assertions against Hashes["sha256"] values (the scraper stores the
// algorithm key separately from the hex content).
const testFullDigestHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		ref        string
		wantName   string
		wantTag    string
		wantDigest string
	}{
		{"vllm", "vllm", "", ""},
		{"vllm:latest", "vllm", "latest", ""},
		{"vllm:v0.6.3", "vllm", "v0.6.3", ""},
		{"vllm@" + testFullDigest, "vllm", "", testFullDigest},
		{"vllm:v0.6.3@" + testFullDigest, "vllm", "v0.6.3", testFullDigest},
		{"gcr.io/p/vllm:tag", "gcr.io/p/vllm", "tag", ""},
		{"gcr.io/p/vllm:tag@" + testFullDigest, "gcr.io/p/vllm", "tag", testFullDigest},
		{"localhost:5000/p/vllm:tag", "localhost:5000/p/vllm", "tag", ""},
		{"localhost:5000/vllm", "localhost:5000/vllm", "", ""},
		{"nvcr.io/nvidia/tritonserver:24.01-py3", "nvcr.io/nvidia/tritonserver", "24.01-py3", ""},
		// Malformed digests: scraper drops them silently rather than
		// passing through to fail downstream schema validation.
		{"vllm@sha256:abc", "vllm", "", ""},                                   // too short
		{"vllm@sha256:0123456789abcdef", "vllm", "", ""},                      // 16 chars, not 64
		{"vllm@md5:0123456789abcdef0123456789abcdef", "vllm", "", ""},         // wrong algorithm
		{"vllm@SHA256:" + testFullDigestHex, "vllm", "", ""},                  // uppercase prefix
		{"vllm@sha256:" + strings.ToUpper(testFullDigestHex), "vllm", "", ""}, // uppercase hex
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			gotName, gotTag, gotDigest := parseImageRef(tc.ref)
			if gotName != tc.wantName || gotTag != tc.wantTag || gotDigest != tc.wantDigest {
				t.Errorf("parseImageRef(%q) = (%q, %q, %q), want (%q, %q, %q)",
					tc.ref, gotName, gotTag, gotDigest, tc.wantName, tc.wantTag, tc.wantDigest)
			}
		})
	}
}

func TestParseImageDigest(t *testing.T) {
	cases := []struct {
		imageID string
		want    string
	}{
		{"", ""},
		{"vllm", ""},
		// Valid 64-char hex digests in each shape.
		{testFullDigest, testFullDigest},
		{"vllm@" + testFullDigest, testFullDigest},
		{"docker-pullable://vllm@" + testFullDigest, testFullDigest},
		{"gcr.io/p/vllm@" + testFullDigest, testFullDigest},
		// Malformed: strict validation drops them so downstream schema
		// validation never has to see them.
		{"sha256:abc", ""},
		{"sha256:0123456789abcdef", ""}, // 16 chars
		{"vllm@sha256:abc", ""},
		{"docker-pullable://vllm@sha256:abc", ""},
		{"sha256:" + strings.ToUpper(testFullDigestHex), ""}, // uppercase hex
		{"SHA256:" + testFullDigestHex, ""},                  // uppercase prefix
		{"sha512:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ""}, // wrong algo, even with 64 hex
	}
	for _, tc := range cases {
		t.Run(tc.imageID, func(t *testing.T) {
			if got := parseImageDigest(tc.imageID); got != tc.want {
				t.Errorf("parseImageDigest(%q) = %q, want %q", tc.imageID, got, tc.want)
			}
		})
	}
}

func TestCleanSHA256Digest(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{testFullDigest, testFullDigest},
		{"sha256:abc", ""},
		{"sha256:" + strings.ToUpper(testFullDigestHex), ""}, // uppercase rejected
		{"SHA256:" + testFullDigestHex, ""},                  // uppercase prefix rejected
		{"sha256:" + testFullDigestHex + "x", ""},            // 65 chars
		{"sha256:" + testFullDigestHex[:63], ""},             // 63 chars
		{"sha256:" + testFullDigestHex[:63] + "g", ""},       // non-hex char
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := cleanSHA256Digest(tc.in); got != tc.want {
				t.Errorf("cleanSHA256Digest(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveContainerDigest_SpecHasDigest(t *testing.T) {
	digest, source := resolveContainerDigest("vllm:v0.6.3@"+testFullDigest, "vllm", nil, false)
	if digest != testFullDigest {
		t.Errorf("digest = %q, want %q", digest, testFullDigest)
	}
	if source != SourceImageReference {
		t.Errorf("source = %q, want %q", source, SourceImageReference)
	}
}

func TestResolveContainerDigest_FromPodStatus(t *testing.T) {
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "docker-pullable://vllm@" + testFullDigest},
			},
		},
	}}
	digest, source := resolveContainerDigest("vllm:v0.6.3", "vllm", pods, false)
	if digest != testFullDigest {
		t.Errorf("digest = %q, want %q", digest, testFullDigest)
	}
	if source != SourcePodStatus {
		t.Errorf("source = %q, want %q", source, SourcePodStatus)
	}
}

func TestResolveContainerDigest_NoMatchingPod(t *testing.T) {
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", ImageID: "vllm@" + testFullDigest},
			},
		},
	}}
	digest, source := resolveContainerDigest("vllm:v0.6.3", "vllm", pods, false)
	if digest != "" || source != "" {
		t.Errorf("expected empty resolution, got (%q, %q)", digest, source)
	}
}

func TestResolveContainerDigest_InitContainer(t *testing.T) {
	initDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	mainDigest := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "model-loader", ImageID: "loader@" + initDigest},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "vllm@" + mainDigest},
			},
		},
	}}
	d, s := resolveContainerDigest("loader:latest", "model-loader", pods, true)
	if d != initDigest || s != SourcePodStatus {
		t.Errorf("init lookup: got (%q, %q), want (%q, pod_status)", d, s, initDigest)
	}
	d, s = resolveContainerDigest("vllm:latest", "vllm", pods, false)
	if d != mainDigest || s != SourcePodStatus {
		t.Errorf("regular lookup: got (%q, %q), want (%q, pod_status)", d, s, mainDigest)
	}
}

func TestResolveContainerDigest_PodStatusReflectsRunningNotSpec(t *testing.T) {
	// The pod is running an OLDER digest than what the spec image string
	// might map to (rollout in progress). The BOM must report the
	// running digest, not anything derived from the spec.
	oldDigest := "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "vllm@" + oldDigest},
			},
		},
	}}
	digest, _ := resolveContainerDigest("vllm:v0.6.3", "vllm", pods, false)
	if digest != oldDigest {
		t.Errorf("expected running digest %q, got %q", oldDigest, digest)
	}
}

func TestResolveContainerDigest_MalformedPodStatusImageIDStaysUnresolved(t *testing.T) {
	// If a pod-status imageID is malformed (truncated, mis-encoded by a
	// custom CRI), the scraper treats it as "no digest" rather than
	// passing the malformed value through to fail BOM schema validation.
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "vllm@sha256:abc"}, // truncated
			},
		},
	}}
	digest, source := resolveContainerDigest("vllm:v0.6.3", "vllm", pods, false)
	if digest != "" {
		t.Errorf("expected empty digest for malformed imageID, got %q", digest)
	}
	if source != "" {
		t.Errorf("expected empty source for malformed imageID, got %q", source)
	}
}

func TestInferenceConfig_DetectRuntime(t *testing.T) {
	cfg := DefaultV1Config()
	cases := []struct {
		image       string
		wantRuntime string
	}{
		// Established patterns (pre-Phase 11)
		{"vllm/vllm-openai:v0.6.3", "vllm"},
		{"ghcr.io/foo/vllm:v0.6.3", "vllm"},
		{"huggingface/text-generation-inference:2.0.0", "tgi"},
		{"nvcr.io/nvidia/tritonserver:24.01-py3", "triton"},
		{"ollama/ollama:0.1.0", "ollama"},
		{"rayproject/ray:latest", ""}, // configured 'ray-project/ray' specifically — narrow pattern
		{"foo/bar:tag", ""},

		// Phase 11 additions
		{"lmsysorg/sglang:v0.3.0", "sglang"},
		{"lmsysorg/sglang-cpu:v0.3.0", "sglang"},
		{"openmmlab/lmdeploy:v0.5.0", "lmdeploy"},
		{"ghcr.io/huggingface/text-embeddings-inference:1.5", "tei"},

		// Conservative-detection guard: similar-named but different
		// projects must NOT match the Phase 11 patterns. See
		// docs/scraper-heuristics.md and the conservative-detection
		// memory entry for rationale.
		{"some-mirror/sglang-but-different:tag", ""},      // not lmsysorg/
		{"openmmlab/mmdeploy:v1.0", ""},                   // mmdeploy != lmdeploy
		{"huggingface/text-embeddings-inference:1.5", ""}, // missing ghcr.io prefix
		// TGI's existing pattern is anchored to Docker Hub form
		// (`^huggingface/text-generation-inference.*`). The GHCR form
		// (`ghcr.io/huggingface/text-generation-inference`) is NOT
		// matched. This is a deliberately preserved false negative:
		// Phase 11 explicitly defers registry-prefix expansion until
		// real customer signal identifies the deployed registries.
		// TGI's pattern is anchored to its Docker Hub repository form
		// (`^huggingface/text-generation-inference.*`). The GHCR variant
		// (`ghcr.io/huggingface/text-generation-inference`) is intentionally
		// not matched. This is a preserved false negative: we defer registry-prefix
		// expansion until real-world usage warrants it, avoiding adding one-off
		// patterns that increase regex maintenance overhead. To support additional
		// registries, update the runtime patterns configuration holistically.
		{"ghcr.io/huggingface/text-generation-inference:2.0.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got, _ := cfg.DetectRuntime(tc.image)
			if got != tc.wantRuntime {
				t.Errorf("DetectRuntime(%q) = %q, want %q", tc.image, got, tc.wantRuntime)
			}
		})
	}
}

func TestInferenceConfig_IsModelVolumePath(t *testing.T) {
	cfg := DefaultV1Config()
	cases := []struct {
		path string
		want bool
	}{
		{"/models", true},
		{"/models/llama", true},
		{"/models-shared", false}, // boundary check: prefix without /
		{"/model", true},
		{"/weights", true},
		{"/checkpoints/run42", true},
		{"/data", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := cfg.IsModelVolumePath(tc.path); got != tc.want {
				t.Errorf("IsModelVolumePath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestInferenceConfig_IsModelEnvVarName(t *testing.T) {
	cfg := DefaultV1Config()
	want := []string{"HF_MODEL_ID", "MODEL_PATH", "MODEL_NAME", "OLLAMA_MODELS", "TRANSFORMERS_CACHE"}
	for _, n := range want {
		if !cfg.IsModelEnvVarName(n) {
			t.Errorf("expected %q in allowlist", n)
		}
	}
	// Case sensitivity
	if cfg.IsModelEnvVarName("hf_model_id") {
		t.Error("env var allowlist must be case sensitive")
	}
	// Unrelated env var
	if cfg.IsModelEnvVarName("PATH") {
		t.Error("PATH should not match the model allowlist")
	}
}

func TestInferenceConfig_IsModelArgFlag(t *testing.T) {
	cfg := DefaultV1Config()
	for _, f := range []string{"--model", "--model-id", "--model-path", "--model-repository", "--model-name"} {
		if !cfg.IsModelArgFlag(f) {
			t.Errorf("expected %q in allowlist", f)
		}
	}
	if cfg.IsModelArgFlag("--port") {
		t.Error("--port should not match the model arg allowlist")
	}
}

func TestDefaultV1Config_LoadsSuccessfully(t *testing.T) {
	c := DefaultV1Config()
	if len(c.RuntimeImagePatterns) == 0 {
		t.Error("expected non-empty RuntimeImagePatterns")
	}
	for _, p := range c.RuntimeImagePatterns {
		if p.compiled == nil {
			t.Errorf("pattern %q (%s) was not compiled", p.Runtime, p.Pattern)
		}
	}
}

func TestLookupVolumeSource(t *testing.T) {
	vols := []corev1.Volume{
		{Name: "pvc-models", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "model-weights"},
		}},
		{Name: "cm-config", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "model-config"},
			},
		}},
		{Name: "host-data", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/data/models"},
		}},
		{Name: "ephemeral", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
	}
	cases := []struct {
		vol      string
		wantName string
		wantKind string
	}{
		{"pvc-models", "model-weights", "persistentVolumeClaim"},
		{"cm-config", "model-config", "configMap"},
		{"host-data", "/data/models", "hostPath"},
		{"ephemeral", "ephemeral", "emptyDir"},
		{"missing", "missing", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.vol, func(t *testing.T) {
			gotName, gotKind := lookupVolumeSource(tc.vol, vols)
			if gotName != tc.wantName || gotKind != tc.wantKind {
				t.Errorf("lookupVolumeSource(%q) = (%q, %q), want (%q, %q)",
					tc.vol, gotName, gotKind, tc.wantName, tc.wantKind)
			}
		})
	}
}
