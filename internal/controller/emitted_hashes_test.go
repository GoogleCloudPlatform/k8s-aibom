package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// The per-process record of emitted input hashes: it only ever answers
// "did this process already emit exactly this document", it is cleared
// when a sink fails or the AIBOM goes away, and copies of a reconciler
// made before first use get their own record.
func TestEmittedHashes_RecordForgetAndLazyInit(t *testing.T) {
	r := &WorkloadReconciler{}
	key := types.NamespacedName{Namespace: "ns", Name: "apps-deployment-vllm"}
	if r.emittedBefore(key, "h1") {
		t.Fatal("nothing recorded yet")
	}
	if r.emittedBefore(key, "") {
		t.Fatal("an empty hash never counts as emitted")
	}
	r.emittedStore().record(key, "h1")
	if !r.emittedBefore(key, "h1") {
		t.Fatal("h1 was recorded")
	}
	if r.emittedBefore(key, "h2") {
		t.Fatal("a different hash is not a repeat")
	}
	r.emittedStore().record(key, "h2")
	if r.emittedBefore(key, "h1") || !r.emittedBefore(key, "h2") {
		t.Fatal("the record holds the last emitted hash only")
	}
	r.emittedStore().forget(key)
	if r.emittedBefore(key, "h2") {
		t.Fatal("forget clears the record (sink failure, deletion)")
	}

	fresh := &WorkloadReconciler{}
	copied := *fresh // struct copy before first use, as the test harnesses do
	copied.emittedStore().record(key, "h3")
	if fresh.emittedBefore(key, "h3") {
		t.Fatal("a copy made before first use must not share the record")
	}
}
