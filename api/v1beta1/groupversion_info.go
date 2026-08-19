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

// Package v1alpha1 contains API Schema definitions for the aibom.k8saibom.dev v1alpha1
// API group: the AIBOM workload BOM resource and the AIBOMControllerConfig
// cluster configuration resource.
//
// +kubebuilder:object:generate=true
// +groupName=aibom.k8saibom.dev
package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// v1beta1 is schema-identical to v1alpha1 by design (Design 001): the
// 1.x field freeze allows conversion strategy None, and this version is
// the storage version from the graduation release onward. Keep the two
// packages in lockstep; field changes are prohibited in both through 1.x.

// GroupVersion is the API Group Version for the aibom.k8saibom.dev v1beta1 API.
var GroupVersion = schema.GroupVersion{Group: "aibom.k8saibom.dev", Version: "v1beta1"}

// SchemeBuilder collects functions that add Go types into a runtime.Scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion} //nolint:staticcheck

// AddToScheme adds every registered type in this package to the given Scheme.
var AddToScheme = SchemeBuilder.AddToScheme
