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

func TestResolveContainerDigest_ForeignImageNameNeverSuppliesDigest(t *testing.T) {
	// A same-named container running a DIFFERENT image must never
	// supply this component's digest — the mis-attribution guard.
	foreign := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	own := "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	pods := []corev1.Pod{
		{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "server", Image: "other/imposter:v1", ImageID: "other/imposter@" + foreign},
		}}},
		{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "server", Image: "vllm/vllm-openai:v0.6.3", ImageID: "vllm/vllm-openai@" + own},
		}}},
	}
	d, srcE := resolveContainerDigest("vllm/vllm-openai:v0.6.3", "server", pods, false)
	if d != own || srcE != SourcePodStatus {
		t.Errorf("got (%q,%q), want own digest from the matching image", d, srcE)
	}
	// Only the foreign pod present: stay unresolved rather than borrow.
	d, srcE = resolveContainerDigest("vllm/vllm-openai:v0.6.3", "server", pods[:1], false)
	if d != "" || srcE != "" {
		t.Errorf("foreign-only candidates must stay unresolved, got (%q,%q)", d, srcE)
	}
}

func TestResolveContainerDigest_DockerHubNormalization(t *testing.T) {
	// Kubelets commonly report the docker.io-qualified form of a Docker
	// Hub image; the guard must treat it as the same name as the spec.
	dg := "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	pods := []corev1.Pod{{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "vllm", Image: "docker.io/vllm/vllm-openai:v0.6.3", ImageID: "docker.io/vllm/vllm-openai@" + dg},
	}}}}
	d, srcE := resolveContainerDigest("vllm/vllm-openai:v0.6.3", "vllm", pods, false)
	if d != dg || srcE != SourcePodStatus {
		t.Errorf("docker.io-qualified status image must match unqualified spec: got (%q,%q)", d, srcE)
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
		{"huggingface/text-generation-inference", "tgi"},
		{"huggingface/text-generation-inference@" + testFullDigest, "tgi"},
		{"huggingface/text-generation-inference-extra:1", ""},
		{"nvcr.io/nvidia/tritonserver:24.01-py3", "triton"},
		{"nvcr.io/nvidia/tritonserver", "triton"},
		{"nvcr.io/nvidia/tritonserver@" + testFullDigest, "triton"},
		{"nvcr.io/nvidia/tritonserver-sidecar:latest", ""},
		{"ollama/ollama:0.1.0", "ollama"},
		{"ollama/ollama", "ollama"},
		{"ollama/ollama@" + testFullDigest, "ollama"},
		{"ollama/ollama-extra:1", ""},
		{"rayproject/ray:latest", "ray-serve"},
		{"rayproject/ray-ml:2.52.0", "ray-serve"},
		{"rayproject/ray-llm:latest", ""},
		{"ray-project/ray:latest", ""},
		{"foo/bar:tag", ""},

		// Phase 11 additions
		{"lmsysorg/sglang:v0.3.0", "sglang"},
		{"lmsysorg/sglang-cpu:v0.3.0", "sglang"},

		// Enumerated SGLang server variants retain tag, digest and bare
		// image references; routers and unlisted suffixes stay quiet.
		{"lmsysorg/sglang", "sglang"},
		{"lmsysorg/sglang@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-cpu", "sglang"},
		{"lmsysorg/sglang-cpu@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-rocm:v0.5.21-rocm724-mi35x-20261005", "sglang"},
		{"lmsysorg/sglang-rocm", "sglang"},
		{"lmsysorg/sglang-rocm@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-jax:v0.1.0rc0", "sglang"},
		{"lmsysorg/sglang-jax", "sglang"},
		{"lmsysorg/sglang-jax@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-omni:dev", "sglang"},
		{"lmsysorg/sglang-omni", "sglang"},
		{"lmsysorg/sglang-omni@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-daily:v0.5.9-rocm720-mi30x-20260316", "sglang"},
		{"lmsysorg/sglang-daily", "sglang"},
		{"lmsysorg/sglang-daily@" + testFullDigest, "sglang"},
		{"lmsysorg/sglang-staging:renderer-b4701d67", ""},
		{"lmsysorg/sglang-staging", ""},
		{"lmsysorg/sglang-staging@" + testFullDigest, ""},
		{"lmsysorg/sglang-router:latest", ""},
		{"lmsysorg/sglang-router@" + testFullDigest, ""},
		{"lmsysorg/sglang-extra:1", ""},
		{"lmsysorg/sglang-cpu-extra:1", ""},
		{"lmsysorg/sglang-rocm-extra:1", ""},
		{"lmsysorg/sglang-jax-extra:1", ""},
		{"lmsysorg/sglang-omni-extra:1", ""},
		{"lmsysorg/sglang-daily-extra:1", ""},
		{"lmsysorg/sglang-staging-extra:1", ""},
		{"openmmlab/lmdeploy:v0.5.0", "lmdeploy"},
		{"openmmlab/lmdeploy", "lmdeploy"},
		{"openmmlab/lmdeploy@" + testFullDigest, "lmdeploy"},
		{"openmmlab/lmdeploy-extra:1", ""},
		{"ghcr.io/huggingface/text-embeddings-inference:1.5", "tei"},
		{"ghcr.io/huggingface/text-embeddings-inference", "tei"},
		{"ghcr.io/huggingface/text-embeddings-inference@" + testFullDigest, "tei"},
		{"ghcr.io/huggingface/text-embeddings-inference-extra:1", ""},

		// Conservative-detection guard: similar-named but different
		// projects must NOT match the Phase 11 patterns. See
		// docs/scraper-heuristics.md and the conservative-detection
		// memory entry for rationale.
		{"some-mirror/sglang-but-different:tag", ""},      // not lmsysorg/
		{"openmmlab/mmdeploy:v1.0", ""},                   // mmdeploy != lmdeploy
		{"huggingface/text-embeddings-inference:1.5", ""}, // missing ghcr.io prefix
		// TGI's GHCR form was a deliberately preserved false negative
		// until real deployment signal arrived; it did (ghcr.io-published
		// TGI observed running with no runtime attribution), so the
		// publisher's own GHCR namespace is now covered.
		{"ghcr.io/huggingface/text-generation-inference:2.0.0", "tgi"},
		{"ghcr.io/huggingface/text-generation-inference", "tgi"},
		{"ghcr.io/huggingface/text-generation-inference@" + testFullDigest, "tgi"},
		{"ghcr.io/huggingface/text-generation-inference-extra:1", ""},

		// NVIDIA NIM and Dynamo backend runtimes. Dynamo workers
		// attribute to the backend that serves the model; the prefix
		// patterns also cover the -nightly image variants. TRT-LLM under
		// Triton stays attributed to triton (distinct namespace — no
		// double-counting with the standalone Dynamo form).
		{"nvcr.io/nim/meta/llama-3.1-8b-instruct:1.3", "nim"},
		{"nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.0", "vllm"},
		{"nvcr.io/nvidia/ai-dynamo/vllm-runtime-nightly:20260701-5245c0f", "vllm"},
		{"nvcr.io/nvidia/ai-dynamo/sglang-runtime:1.4.0", "sglang"},
		{"nvcr.io/nvidia/ai-dynamo/tensorrtllm-runtime:1.3.1-efa", "tensorrt-llm"},
		{"nvcr.io/nvidia/tritonserver:24.05-trtllm-python-py3", "triton"},

		// vLLM CPU release image on the vLLM project's public ECR alias —
		// exact alias and repository only; other aliases, other ECR
		// vLLM images and name-prefix near-misses stay quiet.
		{"public.ecr.aws/q9t5s3a7/vllm-cpu-release-repo:v0.11.2", "vllm"},
		{"public.ecr.aws/q9t5s3a7/vllm-cpu-release-repo@sha256:abc", "vllm"},
		{"public.ecr.aws/q9t5s3a7/vllm-cpu-release-repo-extra:v0.11.2", ""},
		{"public.ecr.aws/someone/vllm-cpu-release-repo:v0.11.2", ""},
		{"public.ecr.aws/q9t5s3a7/other-repo:v0.11.2", ""},

		// AWS Deep Learning Containers vLLM — AWS's own public ECR
		// namespace. EC2, SageMaker, versioned-server, bare, digest and
		// tag-plus-digest forms match. Sibling repositories, a name-prefix
		// lookalike, the same path on a registry no other entry covers, a
		// mirror-prefixed path, a private-account ECR copy and near-miss
		// public ECR namespaces stay quiet.
		{"public.ecr.aws/deep-learning-containers/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-ec2", "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-sagemaker", "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm:server-cuda-v2.6", "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm:server-sagemaker-cuda-v2.6", "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm", "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm@" + testFullDigest, "vllm"},
		{"public.ecr.aws/deep-learning-containers/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-ec2@" + testFullDigest, "vllm"},
		{"public.ecr.aws/deep-learning-containers/pytorch-inference:2.6.0-gpu-py312-cu124-ubuntu22.04-ec2", ""},
		{"public.ecr.aws/deep-learning-containers/vllm-arm64:server-cpu-v1.0", ""},
		{"public.ecr.aws/deep-learning-containers/vllm-extra:0.31.0", ""},
		{"public.ecr.aws/deep-learning-containers/huggingface-vllm:0.30.0-transformers5.15.0-gpu-py312-cu130-ubuntu24.04", ""},
		{"quay.io/deep-learning-containers/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-ec2", ""},
		{"mirror.example.com/public.ecr.aws/deep-learning-containers/vllm:0.31.0", ""},
		{"763104351884.dkr.ecr.us-west-2.amazonaws.com/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-ec2", ""},
		{"public.ecr.aws/someone/vllm:0.31.0", ""},
		{"public.ecr.aws/deep-learning-container/vllm:0.31.0", ""},
		{"public.ecr.aws/aws-deep-learning-containers/vllm:0.31.0", ""},

		// Conservative-detection guards: NVIDIA infrastructure images
		// (controllers and endpoint-pickers, not model serving) and
		// near-miss namespaces must not match. Named explicitly at the
		// downstream distributor's request so the exclusions are on
		// record as decisions, not accidents.
		{"nvcr.io/nvidia/ai-dynamo/dynamo-frontend:1.4.0", ""},
		{"nvcr.io/nvidia/ai-dynamo/kubernetes-operator:1.4.0", ""},
		{"nvcr.io/nvidia/ai-dynamo/epp-image:1.4.0", ""},
		{"nvcr.io/nvidia/cloud-native/k8s-nim-operator:2.0.0", ""},
		{"nvcr.io/nimble/foo:1.0", ""}, // nim/ org only, not nim-prefixed orgs

		// LiteLLM gateway — publisher's own GHCR org, enumerated image
		// variants; name-prefix and other-org near-misses do not match.
		{"ghcr.io/berriai/litellm:main-v1.81.0", "litellm"},
		{"ghcr.io/berriai/litellm-database:main-v1.81.0", "litellm"},
		{"ghcr.io/berriai/litellm-non_root:main-v1.81.0", "litellm"},
		{"ghcr.io/berriai/litellm", "litellm"},
		{"ghcr.io/berriai/litellm@" + testFullDigest, "litellm"},
		{"ghcr.io/berriai/litellm-database", "litellm"},
		{"ghcr.io/berriai/litellm-database@" + testFullDigest, "litellm"},
		{"ghcr.io/berriai/litellm-non_root", "litellm"},
		{"ghcr.io/berriai/litellm-non_root@" + testFullDigest, "litellm"},
		{"ghcr.io/berriai/litellm-extra:1", ""},
		{"ghcr.io/berriai/litellm-database-extra:1", ""},
		{"ghcr.io/berriai/litellm-non_root-extra:1", ""},
		{"ghcr.io/berriai/litellm-database-non_root:1", ""},
		{"ghcr.io/berriai-forks/litellm:1.0", ""},
		{"docker.io/berriai/litellm:main", ""}, // ghcr only: the publisher's canonical registry

		// AMD ROCm vLLM — publisher-anchored to AMD's rocm/ namespace.
		// Tag, digest and KubeAI's path form match; other rocm/* images
		// and name-prefix near-misses do not.
		{"rocm/vllm:rocm7.0.0_vllm_0.11.1_20251103", "vllm"},
		{"rocm/vllm/rocm7.0.0_vllm_0.11.1_20251103", "vllm"},
		{"rocm/vllm@sha256:abc", "vllm"},
		{"rocm/pytorch:latest", ""},
		{"rocm/vllm-dev:nightly", ""},
		{"otheruser/rocm/vllm:1", ""},

		// Infinity embeddings/reranking server — publisher-anchored Docker
		// Hub image. Tag and digest forms match; name-prefix and
		// other-registry near-misses do not. DetectRuntime does not strip
		// a registry prefix, so ghcr.io stays quiet.
		{"michaelf34/infinity:0.0.77", "infinity"},
		{"michaelf34/infinity:0.0.77-cpu", "infinity"},
		{"michaelf34/infinity:0.0.77-rocm", "infinity"},
		{"michaelf34/infinity@sha256:abc", "infinity"},
		{"michaelf34/infinity-extra:1", ""},
		{"otheruser/infinity:0.0.77", ""},
		{"ghcr.io/michaelf34/infinity:0.0.77", ""},

		// faster-whisper-server speech-to-text — publisher-anchored Docker
		// Hub image. Non-version tags (latest-cpu / latest-cuda) and digest
		// forms match; name-prefix and other-publisher near-misses do not.
		{"fedirz/faster-whisper-server:latest-cpu", "faster-whisper"},
		{"fedirz/faster-whisper-server:latest-cuda", "faster-whisper"},
		{"fedirz/faster-whisper-server@sha256:abc", "faster-whisper"},
		{"fedirz/faster-whisper-server-extra:1", ""},
		{"otheruser/faster-whisper-server:latest-cpu", ""},
		{"ghcr.io/fedirz/faster-whisper-server:latest-cpu", ""},

		// vllm/ Docker Hub org, narrowed to the model-server families.
		// vllm/agentic-api (llm-d's OpenAI Responses API layer, executes
		// no model), vllm/vllm-router and vllm/vime stay quiet.
		{"vllm/vllm-openai@sha256:abc", "vllm"},
		{"vllm/vllm-openai-rocm:latest", "vllm"},
		{"vllm/vllm-openai-cpu:v0.11.0", "vllm"},
		{"vllm/vllm-tpu:latest", "vllm"},
		{"vllm/vllm-tpu@sha256:abc", "vllm"},
		{"vllm/vllm-omni:latest", "vllm"},
		{"vllm/vllm-omni-rocm@sha256:abc", "vllm"},
		{"vllm/agentic-api:latest", ""},
		{"vllm/agentic-api@sha256:abc", ""},
		{"vllm/vllm-router:latest", ""},
		{"vllm/vime:latest", ""},
		{"vllm/vime-rocm:latest", ""},

		// llm-d model-server images — accelerator and provider variants
		// attribute as runtime llm-d; infrastructure images in the same
		// org (endpoint picker, sidecar, simulator) stay quiet.
		{"ghcr.io/llm-d/llm-d-cuda:v0.2.0", "llm-d"},
		{"ghcr.io/llm-d/llm-d-gke:v0.2.0", "llm-d"},
		{"ghcr.io/llm-d/llm-d-cuda-dev:v0.2.0", "llm-d"},
		{"ghcr.io/llm-d/llm-d-cuda-dev-ubuntu:v0.3.0", "llm-d"},
		{"ghcr.io/llm-d/llm-d-xpu-sglang:v0.3.0", "llm-d"},
		{"ghcr.io/llm-d/llm-d-xpu-sglang-dev@sha256:abc", "llm-d"},
		{"ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.4.0", ""},
		{"ghcr.io/llm-d/llm-d-router-disagg-sidecar:v0.4.0", ""},
		{"ghcr.io/llm-d/llm-d-inference-sim:v0.5.0", ""},
		{"ghcr.io/llm-d/llm-d-kv-cache-manager:v0.4.0", ""},
		{"ghcr.io/llm-d/llm-d-benchmark:v0.4.0", ""},

		// llama.cpp server — publisher-anchored GHCR images under ggml-org
		// (current) and ggerganov (older). Tag and digest forms match;
		// name-prefix and Docker Hub lookalikes do not. DetectRuntime is
		// tag-independent; server- build numbers are not a runtime version.
		{"ghcr.io/ggml-org/llama.cpp:server-b10680", "llama.cpp"},
		{"ghcr.io/ggml-org/llama.cpp:server-cuda-b10680", "llama.cpp"},
		{"ghcr.io/ggml-org/llama.cpp:server-rocm-b10680", "llama.cpp"},
		{"ghcr.io/ggml-org/llama.cpp@sha256:abc", "llama.cpp"},
		{"ghcr.io/ggerganov/llama.cpp:server", "llama.cpp"},
		{"ghcr.io/ggml-org/llama.cpp-extra:server-b10680", ""},
		{"ggml-org/llama.cpp:server-b10680", ""},
		{"docker.io/ggml-org/llama.cpp:server-b10680", ""},

		// LMCache production-stack default model server — publisher-anchored
		// Docker Hub image. Tag and digest forms match; production-stack
		// infrastructure images and other-org near-misses do not.
		{"lmcache/vllm-openai:latest", "vllm"},
		{"lmcache/vllm-openai@sha256:abc", "vllm"},
		{"lmcache/lmstack-router:latest", ""},
		{"lmcache/lmstack-sidecar:latest", ""},
		{"otheruser/vllm-openai:latest", ""},

		// Vertex AI Model Garden vLLM — GKE tutorials' Vertex vLLM image.
		// Tag (model aliases, dated RCs, model-garden release tags) and
		// digest forms match; sibling images under the same path and the
		// same name under another registry do not.
		{"us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve:gemma4", "vllm"},
		{"us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve:20250819_0916_RC01", "vllm"},
		{"us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve:model-garden.pytorch-vllm-serve-release_20250819_p0", "vllm"},
		{"us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve@sha256:abc", "vllm"},
		{"us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-inference:gemma4", ""},
		{"gcr.io/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve:gemma4", ""},

		// kserve/huggingfaceserver — publisher-anchored Docker Hub image
		// run as a plain Deployment. Tag and digest forms match; KServe
		// infrastructure images under the same org, name-prefix, and
		// other-registry near-misses do not.
		{"kserve/huggingfaceserver:latest", "huggingface"},
		{"kserve/huggingfaceserver@sha256:abc", "huggingface"},
		{"kserve/kserve-controller:latest", ""},
		{"kserve/agent:latest", ""},
		{"kserve/router:latest", ""},
		{"kserve/storage-initializer:latest", ""},
		{"kserve/huggingfaceserver-extra:1", ""},
		{"otheruser/huggingfaceserver:latest", ""},
		{"ghcr.io/kserve/huggingfaceserver:latest", ""},

		// openvino/model_server — Intel's OpenVINO Model Server on Docker
		// Hub. Tag and digest forms match (including versioned releases and
		// accelerator suffixes); other openvino/* images and other
		// registries do not.
		{"openvino/model_server:latest", "openvino-model-server"},
		{"openvino/model_server:2026.4.1", "openvino-model-server"},
		{"openvino/model_server:2026.4.1-gpu", "openvino-model-server"},
		{"openvino/model_server:latest-gpu", "openvino-model-server"},
		{"openvino/model_server:weekly", "openvino-model-server"},
		{"openvino/model_server@sha256:abc", "openvino-model-server"},
		{"openvino/ubuntu24_runtime:latest", ""},
		{"openvino/ubuntu24_dev:latest", ""},
		{"openvino/cvat_server:latest", ""},
		{"docker.io/openvino/model_server:latest", ""},
		{"ghcr.io/openvino/model_server:latest", ""},

		// TensorRT-LLM standalone release image on NGC. GA, rc, dev and
		// digest forms match; the devel build image, a name-prefix
		// lookalike and the same path on another registry stay quiet.
		{"nvcr.io/nvidia/tensorrt-llm/release:1.0.0", "tensorrt-llm"},
		{"nvcr.io/nvidia/tensorrt-llm/release:1.3.0rc9", "tensorrt-llm"},
		{"nvcr.io/nvidia/tensorrt-llm/release:1.4.0rc1.dev202610180007", "tensorrt-llm"},
		{"nvcr.io/nvidia/tensorrt-llm/release@" + testFullDigest, "tensorrt-llm"},
		{"nvcr.io/nvidia/tensorrt-llm/release:1.0.0@" + testFullDigest, "tensorrt-llm"},
		{"nvcr.io/nvidia/tensorrt-llm/devel:1.0.0", ""},
		{"nvcr.io/nvidia/tensorrt-llm/release-extra:1.0.0", ""},
		{"nvcr.io/nvidia/tensorrt-llm:1.0.0", ""},
		{"ghcr.io/nvidia/tensorrt-llm/release:1.0.0", ""},
		{"mirror.example.com/nvcr.io/nvidia/tensorrt-llm/release:1.0.0", ""},

		// Speaches — publisher-anchored GHCR image (successor of
		// fedirz/faster-whisper-server). CPU, CUDA, sha- and digest forms
		// match; name-prefix, other-org and other-registry near-misses
		// do not.
		{"ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cpu", "speaches"},
		{"ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda", "speaches"},
		{"ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3", "speaches"},
		{"ghcr.io/speaches-ai/speaches:sha-24f209c-cpu", "speaches"},
		{"ghcr.io/speaches-ai/speaches@" + testFullDigest, "speaches"},
		{"ghcr.io/speaches-ai/speaches-extra:0.9.0", ""},
		{"ghcr.io/someone/speaches:0.9.0-cpu", ""},
		{"speaches-ai/speaches:0.9.0-cpu", ""},
		{"quay.io/speaches-ai/speaches:0.9.0-cpu", ""},

		// LocalAI — publisher-anchored Docker Hub and Quay images. CPU,
		// GPU, AIO, master and digest forms match on both; name-prefix,
		// other-user, other-registry and sibling-repository near-misses
		// do not.
		{"localai/localai:latest", "localai"},
		{"localai/localai:latest-gpu-nvidia-cuda-12", "localai"},
		{"localai/localai:latest-gpu-vulkan", "localai"},
		{"localai/localai:latest-aio-cpu", "localai"},
		{"localai/localai:master-1792279864-e7cc993", "localai"},
		{"localai/localai@" + testFullDigest, "localai"},
		{"quay.io/go-skynet/local-ai:latest", "localai"},
		{"quay.io/go-skynet/local-ai:latest-gpu-nvidia-cuda-12", "localai"},
		{"quay.io/go-skynet/local-ai@" + testFullDigest, "localai"},
		{"localai/localai-extra:latest", ""},
		{"someone/localai:latest", ""},
		{"ghcr.io/localai/localai:latest", ""},
		{"quay.io/go-skynet/local-ai-extra:latest", ""},
		{"quay.io/go-skynet/other:latest", ""},
		{"quay.io/someone/local-ai:latest", ""},

		// Xinference — publisher-anchored Docker Hub image. GPU, CPU,
		// aarch64, nightly and digest forms match; the sibling
		// pypiserver repository, other xprobe/* images, name-prefix,
		// other-user and other-registry near-misses do not.
		{"xprobe/xinference:v4.0.1", "xinference"},
		{"xprobe/xinference:v4.0.1-cpu", "xinference"},
		{"xprobe/xinference:latest-aarch64", "xinference"},
		{"xprobe/xinference:nightly-main", "xinference"},
		{"xprobe/xinference@sha256:abc", "xinference"},
		{"xprobe/xinference-pypiserver:latest", ""},
		{"xprobe/xagent-backend:latest", ""},
		{"xprobe/xinference-extra:1", ""},
		{"otheruser/xinference:v4.0.1", ""},
		{"ghcr.io/xprobe/xinference:v4.0.1", ""},

		// Aphrodite Engine OpenAI server — publisher-anchored Docker Hub
		// image. Release, commit-hash and digest forms match; sibling
		// alpindale/aphrodite-* images, name-prefix, other-user and
		// other-registry near-misses do not.
		{"alpindale/aphrodite-openai:v0.9.0", "aphrodite"},
		{"alpindale/aphrodite-openai:v0.6.4.post1", "aphrodite"},
		{"alpindale/aphrodite-openai:5f1d2611", "aphrodite"},
		{"alpindale/aphrodite-openai:latest", "aphrodite"},
		{"alpindale/aphrodite-openai@sha256:abc", "aphrodite"},
		{"alpindale/aphrodite-build:latest", ""},
		{"alpindale/aphrodite-engine:latest", ""},
		{"alpindale/aphrodite-dev:latest", ""},
		{"alpindale/aphrodite-openai-extra:1", ""},
		{"otheruser/aphrodite-openai:latest", ""},
		{"ghcr.io/alpindale/aphrodite-openai:latest", ""},
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
	want := []string{"HF_MODEL_ID", "MODEL_NAME", "NIM_MODEL_NAME", "NIM_SERVED_MODEL_NAME"}
	for _, n := range want {
		if !cfg.IsModelEnvVarName(n) {
			t.Errorf("expected %q in allowlist", n)
		}
	}
	// Verify directory-path env vars are excluded
	for _, n := range []string{"MODEL_PATH", "OLLAMA_MODELS", "TRANSFORMERS_CACHE"} {
		if cfg.IsModelEnvVarName(n) {
			t.Errorf("expected %q to be excluded from allowlist", n)
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

// The AICR-reported NIM case (Design 003 review): a NIM image whose
// default model differs from the served one. NIM_MODEL_NAME must
// produce a declared model component — before NIM_* names joined the
// allowlist, such workloads had no declared model signal at all.
func TestExtractEnvVarModelsNIMNames(t *testing.T) {
	cfg := DefaultV1Config()
	s := NewInferenceSpecScraper(nil)
	c := corev1.Container{
		Name:  "nim",
		Image: "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.3.0",
		Env: []corev1.EnvVar{
			{Name: "NIM_MODEL_NAME", Value: "Qwen/Qwen3-0.6B"},
			{Name: "NIM_SERVED_MODEL_NAME", Value: "qwen3"},
		},
	}
	comps := s.extractEnvVarModelsAt(defaultPodSpecLocator, c, false, 0, cfg)
	if len(comps) != 2 {
		t.Fatalf("want 2 model components from NIM_* env vars, got %d: %+v", len(comps), comps)
	}
	if comps[0].Name != "Qwen/Qwen3-0.6B" {
		t.Fatalf("served model not extracted: %+v", comps[0])
	}
}
