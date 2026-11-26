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

// A model shipped as an OCI image volume (KEP-4639) and mounted at a
// model path is named by its image reference, not recorded as an
// "other" volume known only by its pod-local name.
func TestInferenceSpecScraper_ImageVolumeModelNamedByReference(t *testing.T) {
	s := newScraper()
	const ref = "registry.example.com/models/llama-3-8b:v1"
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "vllm",
						Image:        "vllm/vllm-openai:v0.6.3",
						VolumeMounts: []corev1.VolumeMount{{Name: "weights", MountPath: "/models"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "weights",
						VolumeSource: corev1.VolumeSource{Image: &corev1.ImageVolumeSource{Reference: ref}},
					}},
				},
			},
		},
	}
	w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: "vllm", Namespace: "ns", Object: dep}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentData && c.Name == ref &&
			c.Properties["volume.source"] == "image" &&
			c.Properties["volume.name"] == "weights" &&
			c.Properties["volume.mountPath"] == "/models"
	}, "expected data component named by the image volume reference")
}

// A model arg that refers to an env var with $(VAR_NAME), as GKE's vLLM
// and TGI serving samples write it (--model=$(MODEL_ID) with MODEL_ID
// set in env), names the model the kubelet passes after expanding the
// reference, not the reference text. A reference whose value the spec
// does not state (valueFrom, envFrom, undefined) names no model.
func TestInferenceSpecScraper_ArgVariableReferences(t *testing.T) {
	lit := func(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }
	fromSecret := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "hf-secret"}, Key: "hf_api_token",
		}}}
	}
	fromConfigMap := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "models"}, Key: "id",
		}}}
	}
	const locator = "spec.template.spec.containers[0].args"
	cases := []struct {
		name        string
		args        []string
		env         []corev1.EnvVar
		wantModel   string // "" = no model component
		wantLocator string
		wantEnvVar  string // identity.envVarName; "" = not set
	}{
		{
			name:        "GKE sample, joined form",
			args:        []string{"--model=$(MODEL_ID)", "--tensor-parallel-size=1"},
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b"), fromSecret("HUGGING_FACE_HUB_TOKEN")},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[0](--model=)",
			wantEnvVar:  "MODEL_ID",
		},
		{
			name:        "positional form",
			args:        []string{"--model-id", "$(MODEL_ID)", "--num-shard", "1"},
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2b-it")},
			wantModel:   "google/gemma-2b-it",
			wantLocator: locator + "[0 1](--model-id)",
			wantEnvVar:  "MODEL_ID",
		},
		{
			name:        "reference inside a path",
			args:        []string{"--model=/models/$(MODEL_DIR)"},
			env:         []corev1.EnvVar{lit("MODEL_DIR", "gemma-2-2b")},
			wantModel:   "/models/gemma-2-2b",
			wantLocator: locator + "[0](--model=)",
			wantEnvVar:  "MODEL_DIR",
		},
		{
			name:        "several references in one arg",
			args:        []string{"--model=$(ORG)/$(NAME)-$(ORG)"},
			env:         []corev1.EnvVar{lit("ORG", "google"), lit("NAME", "gemma-2-2b")},
			wantModel:   "google/gemma-2-2b-google",
			wantLocator: locator + "[0](--model=)",
			wantEnvVar:  "ORG,NAME",
		},
		{
			name:        "env value refers to an earlier env var",
			args:        []string{"--model=$(MODEL_ID)"},
			env:         []corev1.EnvVar{lit("ORG", "google"), lit("MODEL_ID", "$(ORG)/gemma-2-2b")},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[0](--model=)",
			wantEnvVar:  "MODEL_ID",
		},
		{
			name:        "escaped reference is passed as written",
			args:        []string{"--model=$$(MODEL_ID)"},
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b")},
			wantModel:   "$(MODEL_ID)",
			wantLocator: locator + "[0](--model=)",
		},
		{
			name:        "literal model is unchanged",
			args:        []string{"--model", "meta-llama/Llama-3.1-8B-Instruct"},
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b")},
			wantModel:   "meta-llama/Llama-3.1-8B-Instruct",
			wantLocator: locator + "[0 1](--model)",
		},
		{
			name: "value from a ConfigMap",
			args: []string{"--model=$(MODEL_ID)"},
			env:  []corev1.EnvVar{fromConfigMap("MODEL_ID")},
		},
		{
			name: "undefined variable (envFrom, Service links or none)",
			args: []string{"--model", "$(MODEL_ID)", "--port", "8000"},
		},
		{
			name: "later valueFrom replaces an earlier literal",
			args: []string{"--model=$(MODEL_ID)"},
			env:  []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b"), fromConfigMap("MODEL_ID")},
		},
		{
			name: "env value refers to a later env var",
			args: []string{"--model=$(MODEL_ID)"},
			env:  []corev1.EnvVar{lit("MODEL_ID", "$(ORG)/gemma-2-2b"), lit("ORG", "google")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "vllm-gemma-deployment", Namespace: "ns"},
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "inference-server",
						Image: "vllm/vllm-openai:v0.10.0",
						Args:  tc.args,
						Env:   tc.env,
					}},
				}}},
			}
			w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: dep.Name, Namespace: dep.Namespace, Object: dep}
			got, err := newScraper().Scrape(context.Background(), w, testConfig())
			if err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			models := componentsOf(got.Components, ComponentMLModel)
			if tc.wantModel == "" {
				if len(models) != 0 {
					t.Fatalf("want no model component, got:\n%s", dumpComponents(models))
				}
				return
			}
			if len(models) != 1 {
				t.Fatalf("want one model component %q, got:\n%s", tc.wantModel, dumpComponents(models))
			}
			m := models[0]
			if m.Name != tc.wantModel || m.Confidence != ConfidenceDeclared ||
				m.Evidence.Source != SourceContainerArg || m.Evidence.Locator != tc.wantLocator {
				t.Errorf("model = %s %s {%s %s}, want %s declared {%s %s}",
					m.Name, m.Confidence, m.Evidence.Source, m.Evidence.Locator,
					tc.wantModel, SourceContainerArg, tc.wantLocator)
			}
			if got := m.Properties["identity.envVarName"]; got != tc.wantEnvVar {
				t.Errorf("identity.envVarName = %q, want %q", got, tc.wantEnvVar)
			}
		})
	}
}

// A model env var whose value refers to an earlier env var with
// $(VAR_NAME) names the model the kubelet sets, not the reference text. A
// value whose reference the spec does not state names no model.
func TestInferenceSpecScraper_EnvVariableReferences(t *testing.T) {
	lit := func(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }
	fromConfigMap := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "models"}, Key: "id",
		}}}
	}
	const locator = "spec.template.spec.containers[0].env"
	cases := []struct {
		name        string
		env         []corev1.EnvVar
		wantModel   string // "" = no model component
		wantLocator string
		wantEnvVar  string // identity.envVarName
	}{
		{
			name:        "reference to an earlier env var",
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b"), lit("HF_MODEL_ID", "$(MODEL_ID)")},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[1](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID,MODEL_ID",
		},
		{
			name: "several references in one value",
			env: []corev1.EnvVar{
				lit("ORG", "google"), lit("SIZE", "2b"), lit("HF_MODEL_ID", "$(ORG)/gemma-$(SIZE)-$(ORG)"),
			},
			wantModel:   "google/gemma-2b-google",
			wantLocator: locator + "[2](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID,ORG,SIZE",
		},
		{
			name: "reference to an env var that has one itself",
			env: []corev1.EnvVar{
				lit("ORG", "google"), lit("MODEL_ID", "$(ORG)/gemma-2-2b"), lit("HF_MODEL_ID", "$(MODEL_ID)"),
			},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[2](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID,MODEL_ID",
		},
		{
			name: "the value at that point in the list, not a later one",
			env: []corev1.EnvVar{
				lit("MODEL_ID", "google/gemma-2-2b"), lit("HF_MODEL_ID", "$(MODEL_ID)"), lit("MODEL_ID", "google/gemma-2-9b"),
			},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[1](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID,MODEL_ID",
		},
		{
			name:        "escaped reference is passed as written",
			env:         []corev1.EnvVar{lit("MODEL_ID", "google/gemma-2-2b"), lit("HF_MODEL_ID", "$$(MODEL_ID)")},
			wantModel:   "$(MODEL_ID)",
			wantLocator: locator + "[1](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID",
		},
		{
			name:        "literal model is unchanged",
			env:         []corev1.EnvVar{lit("ORG", "google"), lit("HF_MODEL_ID", "google/gemma-2-2b")},
			wantModel:   "google/gemma-2-2b",
			wantLocator: locator + "[1](HF_MODEL_ID)",
			wantEnvVar:  "HF_MODEL_ID",
		},
		{
			name: "reference to a ConfigMap value",
			env:  []corev1.EnvVar{fromConfigMap("MODEL_ID"), lit("HF_MODEL_ID", "$(MODEL_ID)")},
		},
		{
			name: "undefined variable (envFrom, Service links or none)",
			env:  []corev1.EnvVar{lit("HF_MODEL_ID", "google/$(NAME)")},
		},
		{
			name: "reference to a later env var",
			env:  []corev1.EnvVar{lit("HF_MODEL_ID", "$(ORG)/gemma-2-2b"), lit("ORG", "google")},
		},
		{
			name: "reference to an empty value",
			env:  []corev1.EnvVar{lit("MODEL_ID", ""), lit("HF_MODEL_ID", "$(MODEL_ID)")},
		},
		{
			name: "value from a ConfigMap",
			env:  []corev1.EnvVar{fromConfigMap("HF_MODEL_ID")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gemma-deployment", Namespace: "ns"},
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "inference-server",
						Image: "vllm/vllm-openai:v0.10.0",
						Env:   tc.env,
					}},
				}}},
			}
			w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: dep.Name, Namespace: dep.Namespace, Object: dep}
			got, err := newScraper().Scrape(context.Background(), w, testConfig())
			if err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			models := componentsOf(got.Components, ComponentMLModel)
			if tc.wantModel == "" {
				if len(models) != 0 {
					t.Fatalf("want no model component, got:\n%s", dumpComponents(models))
				}
				return
			}
			if len(models) != 1 {
				t.Fatalf("want one model component %q, got:\n%s", tc.wantModel, dumpComponents(models))
			}
			m := models[0]
			if m.Name != tc.wantModel || m.Confidence != ConfidenceInferred ||
				m.Evidence.Source != SourceEnvVar || m.Evidence.Locator != tc.wantLocator {
				t.Errorf("model = %s %s {%s %s}, want %s inferred {%s %s}",
					m.Name, m.Confidence, m.Evidence.Source, m.Evidence.Locator,
					tc.wantModel, SourceEnvVar, tc.wantLocator)
			}
			if got := m.Properties["identity.envVarName"]; got != tc.wantEnvVar {
				t.Errorf("identity.envVarName = %q, want %q", got, tc.wantEnvVar)
			}
		})
	}
}

func TestExpandVarRefs(t *testing.T) {
	vars := map[string]string{"A": "x", "EMPTY": ""}
	lookup := func(name string) (string, bool) {
		v, found := vars[name]
		return v, found
	}
	cases := []struct {
		in, want string
		wantOK   bool
	}{
		{"no reference", "no reference", true},
		{"$(A)", "x", true},
		{"pre-$(A)-post", "pre-x-post", true},
		{"$(A)$(EMPTY)$(A)", "xx", true},
		{"$(A", "$(A", true},            // unterminated
		{"$(A $(A)", "$(A $(A)", false}, // the name runs to the first ")"
		{"a$b", "a$b", true},            // lone $
		{"abc$", "abc$", true},          // trailing $
		{"$", "$", true},                // only $
		{"$()", "$()", false},           // empty name is never defined
		{"$$", "$", true},               // escape
		{"$$(A)", "$(A)", true},         // escaped reference
		{"$$$(A)", "$x", true},          // escape, then reference
		{"$(MISSING)", "$(MISSING)", false},
		{"$(A)-$(MISSING)", "x-$(MISSING)", false},
	}
	for _, tc := range cases {
		got, ok := expandVarRefs(tc.in, lookup)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("expandVarRefs(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestSpecEnvValues(t *testing.T) {
	lit := func(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }
	from := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		}}
	}
	cases := []struct {
		name string
		env  []corev1.EnvVar
		want map[string]string
	}{
		{"no env", nil, map[string]string{}},
		{"later literal overrides an earlier one",
			[]corev1.EnvVar{lit("A", "1"), lit("A", "2")}, map[string]string{"A": "2"}},
		{"later literal overrides an earlier valueFrom",
			[]corev1.EnvVar{from("A"), lit("A", "2")}, map[string]string{"A": "2"}},
		{"later valueFrom drops an earlier literal",
			[]corev1.EnvVar{lit("A", "1"), from("A")}, map[string]string{}},
		{"reference to an earlier entry",
			[]corev1.EnvVar{lit("B", "1"), lit("A", "$(B)/x")}, map[string]string{"A": "1/x", "B": "1"}},
		{"reference to a later entry is not resolved",
			[]corev1.EnvVar{lit("A", "$(B)"), lit("B", "1")}, map[string]string{"B": "1"}},
		{"reference to the earlier value of the same name",
			[]corev1.EnvVar{lit("A", "1"), lit("A", "$(A)2")}, map[string]string{"A": "12"}},
		{"reference to a valueFrom entry is not resolved",
			[]corev1.EnvVar{from("B"), lit("A", "$(B)")}, map[string]string{}},
		{"escaped reference",
			[]corev1.EnvVar{lit("B", "1"), lit("A", "$$(B)")}, map[string]string{"A": "$(B)", "B": "1"}},
		{"unterminated, lone and trailing $",
			[]corev1.EnvVar{lit("A", "$(B"), lit("C", "a$b$")}, map[string]string{"A": "$(B", "C": "a$b$"}},
		{"empty reference",
			[]corev1.EnvVar{lit("A", "$()")}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := specEnvValues(tc.env); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("specEnvValues = %v, want %v", got, tc.want)
			}
		})
	}
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

// The same pods listed in a different order must give the same
// document and input hash; otherwise every reconcile of a workload
// whose replicas run different digests re-sends the document.
func TestInferenceSpecScraper_InputHashDoesNotDependOnPodOrder(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:latest"},
					},
				},
			},
		},
	}
	pod := func(name, digest string) corev1.Pod {
		p := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "vllm", Image: "docker.io/vllm/vllm-openai:latest", ImageID: "docker.io/vllm/vllm-openai@" + digest},
		}}}
		p.Name = name
		return p
	}
	a := pod("vllm-a", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := pod("vllm-b", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	var hashes []string
	for _, pods := range [][]corev1.Pod{{a, b}, {b, a}} {
		got, err := s.Scrape(context.Background(), Workload{
			Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Object: dep,
			Pods:   pods,
		}, testConfig())
		if err != nil {
			t.Fatal(err)
		}
		h, err := HashBOMInputs(got)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	if hashes[0] != hashes[1] {
		t.Errorf("input hash depends on pod order: %s (a, b) vs %s (b, a)", hashes[0], hashes[1])
	}
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
