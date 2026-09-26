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
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// Regression tests for the pod-attribution fix: digests enter a BOM
// only from pods positively tied to the workload via the controller
// ownerReference chain, never from selector overlap alone. The
// pre-fix defect: two same-namespace Deployments with overlapping
// selectors and a common container name cross-contaminated digests,
// and any principal with pod-create in an opted-in namespace could
// plant a chosen digest in another workload's record.

func boolPtr(b bool) *bool { return &b }

func controllerRef(kind, name string, uid types.UID) metav1.OwnerReference {
	apiVersion := "apps/v1"
	if kind == "Job" {
		apiVersion = "batch/v1"
	}
	return metav1.OwnerReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		UID:        uid,
		Controller: boolPtr(true),
	}
}

func TestIntegration_PodAttribution_OwnershipNotSelector(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "pod-attribution"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)

	const (
		victimDigest   = "1111111111111111111111111111111111111111111111111111111111111111"
		intruderDigest = "2222222222222222222222222222222222222222222222222222222222222222"
		plantedDigest  = "3333333333333333333333333333333333333333333333333333333333333333"
	)

	mkDeployment := func(name, image string, selector map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsName},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "server", Image: image}},
					},
				},
			},
		}
	}

	victimSel := map[string]string{"team": "ml"}
	intruderSel := map[string]string{"team": "ml", "variant": "b"}

	victim := mkDeployment("victim", "vllm/vllm-openai:v0.6.1", victimSel)
	intruder := mkDeployment("intruder", "vllm/vllm-openai:v0.6.2", intruderSel)
	mustCreate(t, env.k8sClient, ctx, victim)
	mustCreate(t, env.k8sClient, ctx, intruder)

	// envtest runs no deployment controller: build the ownership chain
	// by hand, exactly as it exists in a real cluster.
	mkRS := func(name string, dep *appsv1.Deployment, selector map[string]string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: nsName, Labels: selector,
				OwnerReferences: []metav1.OwnerReference{
					controllerRef("Deployment", dep.Name, dep.UID),
				},
			},
			Spec: appsv1.ReplicaSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec:       dep.Spec.Template.Spec,
				},
			},
		}
	}
	victimRS := mkRS("victim-rs", victim, victimSel)
	intruderRS := mkRS("intruder-rs", intruder, intruderSel)
	mustCreate(t, env.k8sClient, ctx, victimRS)
	mustCreate(t, env.k8sClient, ctx, intruderRS)

	mkPod := func(name, image, digestHex string, labels map[string]string, owner []metav1.OwnerReference) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: nsName, Labels: labels, OwnerReferences: owner,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "server", Image: image}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:    "server",
					Image:   image,
					ImageID: image + "@sha256:" + digestHex,
				}},
			},
		}
	}

	// Pod names chosen so the foreign pods sort FIRST in list order —
	// the pre-fix code took the first name-match in list order.
	pods := []*corev1.Pod{
		// A bare planted pod matching the victim's selector: the
		// adversarial case. No owner; must never contribute.
		mkPod("aaa-planted", "vllm/vllm-openai:v0.6.1", plantedDigest, victimSel, nil),
		// The intruder's real pod; matches the victim's selector too.
		mkPod("bbb-intruder", "vllm/vllm-openai:v0.6.2", intruderDigest, intruderSel,
			[]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: intruderRS.Name, UID: intruderRS.UID, Controller: boolPtr(true)}}),
		// The victim's own pod, sorting last.
		mkPod("zzz-victim", "vllm/vllm-openai:v0.6.1", victimDigest, victimSel,
			[]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: victimRS.Name, UID: victimRS.UID, Controller: boolPtr(true)}}),
	}
	for _, p := range pods {
		st := p.Status.DeepCopy()
		mustCreate(t, env.k8sClient, ctx, p)
		p.Status = *st
		if err := env.k8sClient.Status().Update(ctx, p); err != nil {
			t.Fatalf("update pod status %s: %v", p.Name, err)
		}
	}

	readInline := func(name string) (string, error) {
		var a aibomv1beta1.AIBOM
		if err := env.k8sClient.Get(ctx, types.NamespacedName{
			Name: "apps-deployment-" + name, Namespace: nsName,
		}, &a); err != nil {
			return "", err
		}
		if a.Status.BOMDocument == nil || a.Status.BOMDocument.Inline == nil {
			return "", errors.New("inline BOM not yet populated")
		}
		return string(a.Status.BOMDocument.Inline.Data), nil
	}

	var victimBOM, intruderBOM string
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		var err error
		if victimBOM, err = readInline("victim"); err != nil {
			return err
		}
		if intruderBOM, err = readInline("intruder"); err != nil {
			return err
		}
		if !strings.Contains(victimBOM, victimDigest) {
			return errors.New("victim BOM does not yet carry its own digest")
		}
		return nil
	})

	for _, bad := range []struct{ where, digest, label string }{
		{"victim", intruderDigest, "intruder's"},
		{"victim", plantedDigest, "planted"},
		{"intruder", victimDigest, "victim's"},
		{"intruder", plantedDigest, "planted"},
	} {
		bom := victimBOM
		if bad.where == "intruder" {
			bom = intruderBOM
		}
		if strings.Contains(bom, bad.digest) {
			t.Errorf("%s BOM contains the %s digest %s — cross-contamination",
				bad.where, bad.label, bad.digest[:12])
		}
	}
	if !strings.Contains(intruderBOM, intruderDigest) {
		t.Errorf("intruder BOM does not carry its own digest")
	}
}

func TestFilterPodsOwnedBy_DirectOwnershipKinds(t *testing.T) {
	mk := func(name string, owner []metav1.OwnerReference) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, OwnerReferences: owner}}
	}
	workloadUID := types.UID("workload-uid")
	otherUID := types.UID("other-uid")

	pods := []corev1.Pod{
		mk("bare-planted", nil),
		mk("foreign", []metav1.OwnerReference{{Kind: "Job", Name: "other", UID: otherUID, Controller: boolPtr(true)}}),
		mk("non-controller-ref", []metav1.OwnerReference{{Kind: "Job", Name: "w", UID: workloadUID}}),
		mk("own", []metav1.OwnerReference{{Kind: "Job", Name: "w", UID: workloadUID, Controller: boolPtr(true)}}),
	}
	got := filterPodsOwnedBy(pods, singleOwner(workloadUID))
	if len(got) != 1 || got[0].Name != "own" {
		names := make([]string, len(got))
		for i := range got {
			names[i] = got[i].Name
		}
		t.Fatalf("filterPodsOwnedBy = %v, want exactly [own]: bare, foreign-owned and non-controller refs must all be excluded", names)
	}
	if out := filterPodsOwnedBy(pods, nil); out != nil {
		t.Fatalf("empty owner set must select nothing, got %d pods", len(out))
	}
}
