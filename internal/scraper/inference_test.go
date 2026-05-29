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
	"context"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var fixedTime = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

// newScraper returns an InferenceSpecScraper with NoopVerifier and a
// fixed-time clock so test outputs are deterministic. Tests pass
// testConfig() to Scrape calls; the scraper itself no longer holds a
// config (per Checkpoint 5: configuration is per-call).
func newScraper() *InferenceSpecScraper {
	s := NewInferenceSpecScraper(nil)
	s.now = func() time.Time { return fixedTime }
	return s
}

// testConfig is the standard *InferenceConfig used by Scrape calls in
// this package's tests — the embedded v1 defaults. Centralized so
// future changes to the default-config shape don't require touching
// every Scrape call site.
func testConfig() *InferenceConfig { return DefaultV1Config() }

func TestInferenceSpecScraper_Name(t *testing.T) {
	if got := newScraper().Name(); got != "inference.spec" {
		t.Errorf("Name() = %q, want %q", got, "inference.spec")
	}
}

func TestInferenceSpecScraper_HandlesKind(t *testing.T) {
	s := newScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, true},
		// KServe has its own scraper; InferenceSpecScraper deliberately
		// does NOT handle it (different shape — declared runtime + storage
		// URI rather than pod template).
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}, false},
		// Batch workloads are out-of-scope for the inference-shaped
		// scraper. v1.0 positions as inference; Job + CronJob land in
		// a dedicated batch phase.
		{WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"}, false},
		{WorkloadKind{Group: "batch", Version: "v1", Kind: "CronJob"}, false},
		{WorkloadKind{}, false},
	}
	for _, tc := range cases {
		name := tc.kind.Kind
		if name == "" {
			name = "zero"
		}
		t.Run(name, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestInferenceSpecScraper_UnsupportedKind(t *testing.T) {
	// Passing an unsupported object type returns an error rather than
	// silently emitting an empty BOMInputs. The discovery layer is
	// responsible for only invoking scrapers for kinds they
	// HandlesKind-claim. Use a Pod here as a representative
	// "definitely-not-handled" type (we don't want to use a type the
	// scraper now handles, like StatefulSet, since the test would lose
	// meaning if it pretends Pod is unsupported instead of testing the
	// unsupported case).
	s := newScraper()
	_, err := s.Scrape(context.Background(), Workload{
		Kind:   WorkloadKind{Group: "", Version: "v1", Kind: "Pod"},
		Object: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x"}},
	}, testConfig())
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}

func TestInferenceSpecScraper_VLLMDeployment_HappyPath(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vllm-llama3",
			Namespace: "prod-inference",
			UID:       types.UID("dep-123"),
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "vllm",
						Image: "vllm/vllm-openai:v0.6.3",
						Args:  []string{"--model", "meta-llama/Llama-3.1-8B-Instruct", "--port", "8000"},
						Env: []corev1.EnvVar{
							{Name: "HF_MODEL_ID", Value: "meta-llama/Llama-3.1-8B-Instruct"},
							{Name: "PORT", Value: "8000"},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "model-store", MountPath: "/models"},
							{Name: "tmp", MountPath: "/tmp"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "model-store", VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "llama-weights"},
						}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						}},
					},
				},
			},
		},
	}
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "docker-pullable://vllm/vllm-openai@" + testFullDigest},
			},
		},
	}}
	w := Workload{
		Kind:      WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Category:  CategoryInference,
		Namespace: dep.Namespace,
		Name:      dep.Name,
		UID:       dep.UID,
		Object:    dep,
		Pods:      pods,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if got.ScraperName != "inference.spec" {
		t.Errorf("ScraperName = %q", got.ScraperName)
	}
	if !got.ScrapeTimestamp.Equal(fixedTime) {
		t.Errorf("ScrapeTimestamp = %v, want %v", got.ScrapeTimestamp, fixedTime)
	}
	// Mix of Declared (--model arg, image-pattern runtime, container digest)
	// and Inferred (HF_MODEL_ID env var, PVC-mounted-at-/models volume).
	// Per the aggregation rule, lowest tier wins → workload = Inferred.
	if got.Confidence != ConfidenceInferred {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceInferred)
	}
	if len(got.Provenance) != 1 {
		t.Fatalf("Provenance = %d entries, want 1", len(got.Provenance))
	}
	if got.Provenance[0].ScrapeMethod != "spec" {
		t.Errorf("ScrapeMethod = %q, want %q", got.Provenance[0].ScrapeMethod, "spec")
	}
	// Expected components after sortComponents:
	//  - application: "vllm" (runtime detection from image pattern)
	//  - container: "vllm/vllm-openai" (with sha256 digest from pod status)
	//  - data: "llama-weights" (PVC mounted at /models)
	//  - machine-learning-model: "meta-llama/Llama-3.1-8B-Instruct" (--model arg, Declared)
	//  - machine-learning-model: "meta-llama/Llama-3.1-8B-Instruct" (HF_MODEL_ID env, Inferred)
	if len(got.Components) != 5 {
		t.Fatalf("len(Components) = %d, want 5\ngot:\n%s", len(got.Components), dumpComponents(got.Components))
	}
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentApplication && c.Name == "vllm" &&
			c.Properties["runtime.name"] == "vllm" &&
			c.Confidence == ConfidenceInferred && // pattern-matched runtime is always Inferred
			c.Version == "v0.6.3" && // v0.6.3 is semver-shaped, so version is populated
			c.Evidence.Source == SourceImagePattern
	}, "expected runtime application component for vllm (Inferred, version v0.6.3, from image_pattern)")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentContainer && c.Name == "vllm/vllm-openai" &&
			c.Version == "v0.6.3" && c.Hashes["sha256"] == testFullDigestHex &&
			c.Confidence == ConfidenceDeclared && c.Evidence.Source == SourcePodStatus
	}, "expected container component with digest from pod status")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentData && c.Name == "llama-weights" &&
			c.Properties["volume.source"] == "persistentVolumeClaim"
	}, "expected data component for PVC")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel && c.Name == "meta-llama/Llama-3.1-8B-Instruct" &&
			c.Confidence == ConfidenceDeclared && c.Evidence.Source == SourceContainerArg
	}, "expected declared model component from --model arg")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel && c.Name == "meta-llama/Llama-3.1-8B-Instruct" &&
			c.Confidence == ConfidenceInferred && c.Evidence.Source == SourceEnvVar
	}, "expected inferred model component from HF_MODEL_ID env var")
}

func TestInferenceSpecScraper_DigestUnresolvedWhenNoReadyPod(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3"},
					},
				},
			},
		},
	}
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
		Pods:   nil, // no running pods
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	// Container component must be Unresolved (per A1).
	var container *Component
	for i := range got.Components {
		if got.Components[i].Type == ComponentContainer {
			container = &got.Components[i]
			break
		}
	}
	if container == nil {
		t.Fatal("missing container component")
	}
	if container.Confidence != ConfidenceUnresolved {
		t.Errorf("container.Confidence = %q, want %q (no digest source available)",
			container.Confidence, ConfidenceUnresolved)
	}
	if container.Hashes != nil {
		t.Errorf("container.Hashes should be nil for unresolved digest, got %v", container.Hashes)
	}
	// The runtime application Component is Inferred (pattern-matched), the
	// container is Unresolved. Per the aggregation rule, Unresolved is
	// excluded; the lowest tier among remaining non-Unresolved attributes is
	// Inferred → workload-level confidence is Inferred.
	if got.Confidence != ConfidenceInferred {
		t.Errorf("workload Confidence = %q, want %q (runtime app component is Inferred; container is Unresolved and excluded)",
			got.Confidence, ConfidenceInferred)
	}
}

func TestInferenceSpecScraper_RunningDigestDiffersFromSpec(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3"},
					},
				},
			},
		},
	}
	// Spec says v0.6.3 but the running pod has an older digest (rollout in progress).
	oldDigest := "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	oldHex := "8888888888888888888888888888888888888888888888888888888888888888"
	pods := []corev1.Pod{{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ImageID: "vllm/vllm-openai@" + oldDigest},
			},
		},
	}}
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
		Pods:   pods,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Components {
		if c.Type == ComponentContainer {
			if c.Hashes["sha256"] != oldHex {
				t.Errorf("expected running digest hex %q, got %v", oldHex, c.Hashes)
			}
			if c.Evidence.Source != SourcePodStatus {
				t.Errorf("expected Evidence.Source = pod_status, got %q", c.Evidence.Source)
			}
			return
		}
	}
	t.Fatal("missing container component")
}

func TestInferenceSpecScraper_DigestFromSpec(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai@" + testFullDigest},
					},
				},
			},
		},
	}
	w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Object: dep}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Components {
		if c.Type == ComponentContainer {
			if c.Hashes["sha256"] != testFullDigestHex {
				t.Errorf("expected spec digest hex %q, got %v", testFullDigestHex, c.Hashes)
			}
			if c.Evidence.Source != SourceImageReference {
				t.Errorf("expected Evidence.Source = image_reference, got %q", c.Evidence.Source)
			}
			return
		}
	}
	t.Fatal("missing container component")
}

func TestInferenceSpecScraper_AnnotationModels(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "x", Namespace: "ns",
			Annotations: map[string]string{
				"model.k8saibom.dev/artifact": "meta-llama/Llama-3.1-8B-Instruct",
				"model.k8saibom.dev/family":   "llama",
				"app.kubernetes.io/owner":     "alice", // must be ignored
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"model.k8saibom.dev/source": "huggingface",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3"},
					},
				},
			},
		},
	}
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	// We expect exactly three ML-model components from annotations:
	// two from workload (artifact, family), one from pod template (source).
	var modelsFromAnnotations []Component
	for _, c := range got.Components {
		if c.Type != ComponentMLModel {
			continue
		}
		if c.Evidence.Source == SourceWorkloadAnnotation || c.Evidence.Source == SourcePodTemplateAnnotation {
			modelsFromAnnotations = append(modelsFromAnnotations, c)
		}
	}
	if len(modelsFromAnnotations) != 3 {
		t.Errorf("len(annotation-sourced models) = %d, want 3", len(modelsFromAnnotations))
		t.Logf("got:\n%s", dumpComponents(got.Components))
	}
}

func TestInferenceSpecScraper_Deterministic(t *testing.T) {
	// Same Workload twice must produce byte-equal BOMInputs modulo
	// ScrapeTimestamp (which we pin via the fixed clock here).
	s := newScraper()
	dep := vllmDeploymentFixture()
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
		Pods: []corev1.Pod{{
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "vllm", ImageID: "vllm/vllm-openai@" + testFullDigest},
				},
			},
		}},
	}
	first, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Scrape is not deterministic.\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

func TestInferenceSpecScraper_InitContainersImageOnly(t *testing.T) {
	// Per "honest, not clever": init containers' env vars and args are
	// NOT scraped for model claims in v1. Only the image is captured.
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{
						Name:  "model-loader",
						Image: "loader:v1@" + testFullDigest,
						Args:  []string{"--model", "should-not-be-extracted"},
						Env:   []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "should-not-be-extracted"}},
					}},
					Containers: []corev1.Container{{
						Name:  "vllm",
						Image: "vllm/vllm-openai:v0.6.3@" + testFullDigest,
					}},
				},
			},
		},
	}
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	// We must see a container Component for the init container, but NO
	// ml-model Components produced from the init container's env or args.
	var initContainer, vllmContainer *Component
	for i := range got.Components {
		c := &got.Components[i]
		if c.Type == ComponentContainer && c.Properties["container.init"] == "true" {
			initContainer = c
		}
		if c.Type == ComponentContainer && c.Properties["container.init"] == "false" && c.Properties["container.name"] == "vllm" {
			vllmContainer = c
		}
		if c.Type == ComponentMLModel && c.Name == "should-not-be-extracted" {
			t.Errorf("init container env/args were incorrectly scraped: %+v", c)
		}
	}
	if initContainer == nil {
		t.Error("missing init container component")
	}
	if vllmContainer == nil {
		t.Error("missing vllm container component")
	}
}

// ---------- helpers ----------

func mustFindOne(t *testing.T, cs []Component, pred func(Component) bool, msg string) {
	t.Helper()
	matches := 0
	for _, c := range cs {
		if pred(c) {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("%s: found %d matches in:\n%s", msg, matches, dumpComponents(cs))
	}
}

func dumpComponents(cs []Component) string {
	out := ""
	for i, c := range cs {
		out += "  [" + itoa(i) + "] type=" + string(c.Type) +
			" name=" + c.Name +
			" version=" + c.Version +
			" confidence=" + string(c.Confidence) +
			" evidence={" + string(c.Evidence.Source) + " " + c.Evidence.Locator + "}\n"
	}
	return out
}

func itoa(i int) string {
	// minimal, avoiding strconv to keep this helper trivially auditable
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func vllmDeploymentFixture() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm-llama3", Namespace: "prod-inference"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "vllm",
						Image: "vllm/vllm-openai:v0.6.3",
						Args:  []string{"--model", "meta-llama/Llama-3.1-8B-Instruct"},
					}},
				},
			},
		},
	}
}
