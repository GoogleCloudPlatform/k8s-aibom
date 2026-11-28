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
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	configpkg "github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// podInfoPods returns the pod label of every aibom_pod_info series
// published for one workload, sorted.
func podInfoPods(t *testing.T, ns, kind, name string) []string {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.PodInfo)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather aibom_pod_info: %v", err)
	}
	var pods []string
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["namespace"] == ns && labels["workload_kind"] == kind && labels["workload_name"] == name {
				pods = append(pods, labels["pod"])
			}
		}
	}
	slices.Sort(pods)
	return pods
}

// TestReconcile_JobPodInfoListsOnlyOwnedPods pins that aibom_pod_info
// (Design 011) carries one series per pod the Job or CronJob owns and
// nothing else. Both reconcilers used to append a Pod built from the
// pod template to the workload's pods. No scraper read it, but the
// identity series did: every Job and CronJob AIBOM published an
// aibom_pod_info series with pod="", which joins to no pod.
func TestReconcile_JobPodInfoListsOnlyOwnedPods(t *testing.T) {
	const ns = "train-team"
	ctx := context.Background()
	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "train"}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "train", Image: "pytorch/pytorch:2.4.0-cuda12.1-cudnn9-runtime"}},
		},
	}
	ownedPod := func(name string, owner metav1.OwnerReference) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, Labels: template.Labels,
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			Spec: template.Spec,
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "finetune", Namespace: ns, UID: "uid-job", Generation: 1},
		Spec: batchv1.JobSpec{
			Selector: &metav1.LabelSelector{MatchLabels: template.Labels},
			Template: template,
		},
	}
	cron := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns, UID: "uid-cron", Generation: 1},
		Spec: batchv1.CronJobSpec{
			Schedule:    "0 2 * * *",
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: template}},
		},
	}
	cronRun := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-29000000", Namespace: ns, UID: "uid-cron-run",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cron, batchv1.SchemeGroupVersion.WithKind("CronJob"))},
		},
		Spec: batchv1.JobSpec{Template: template},
	}

	cases := []struct {
		name      string
		kind      string
		objs      []client.Object
		reconcile func(WorkloadReconciler) error
		want      []string
	}{
		{
			name: "Job",
			kind: "Job",
			objs: []client.Object{job, ownedPod("finetune-x7k2p", *metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job")))},
			reconcile: func(w WorkloadReconciler) error {
				_, err := (&JobReconciler{WorkloadReconciler: w}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: job.Name}})
				return err
			},
			want: []string{"finetune-x7k2p"},
		},
		{
			name: "CronJob",
			kind: "CronJob",
			objs: []client.Object{cron, cronRun, ownedPod("nightly-29000000-abcde", *metav1.NewControllerRef(cronRun, batchv1.SchemeGroupVersion.WithKind("Job")))},
			reconcile: func(w WorkloadReconciler) error {
				_, err := (&CronJobReconciler{WorkloadReconciler: w}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: cron.Name}})
				return err
			},
			want: []string{"nightly-29000000-abcde"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   ns,
				Labels: map[string]string{configpkg.DefaultNamespaceOptInLabel: "true"},
			}}
			c := fake.NewClientBuilder().
				WithScheme(newConfigFakeScheme(t)).
				WithObjects(append([]client.Object{nsObj}, tc.objs...)...).
				WithStatusSubresource(&aibomv1beta1.AIBOM{}).
				Build()
			w := WorkloadReconciler{
				Client: c,
				Scheme: c.Scheme(),
				// The manager's production scraper set (cmd/manager/main.go).
				Scraper: scraper.NewMultiScraper(
					scraper.NewInferenceSpecScraper(nil),
					scraper.NewVectorDBSpecScraper(),
					scraper.NewAgentSpecScraper(),
					scraper.NewTrainingSpecScraper(),
					scraper.NewEvalSpecScraper(),
				),
				BOMBuilder:        bom.NewBuilder(),
				StatusBuilder:     NewStatusBuilder(),
				ConfigStore:       configpkg.NewStore(configpkg.DefaultSnapshot()),
				ControllerName:    "k8s-aibom",
				ControllerVersion: "0.1.0-test",
			}
			if err := tc.reconcile(w); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			aibomKey := types.NamespacedName{Namespace: ns, Name: AIBOMNameForWorkload("batch", tc.kind, tc.objs[0].GetName())}
			t.Cleanup(func() { metrics.ForgetWorkloadInfo(aibomKey.String()) })

			var a aibomv1beta1.AIBOM
			if err := c.Get(ctx, aibomKey, &a); err != nil {
				t.Fatalf("get AIBOM: %v", err)
			}
			if got := podInfoPods(t, ns, tc.kind, tc.objs[0].GetName()); !slices.Equal(got, tc.want) {
				t.Errorf("aibom_pod_info pods = %q, want %q (only the pods the %s owns)", got, tc.want, tc.kind)
			}
		})
	}
}
