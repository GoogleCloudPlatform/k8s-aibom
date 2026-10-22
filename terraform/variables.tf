# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

variable "project_id" {
  description = "The GCP Project ID where the Artifact Registry will be created and the image built."
  type        = string
}

variable "region" {
  description = "The GCP region to deploy the Artifact Registry repository."
  type        = string
  default     = "us-central1"
}

variable "cluster_name" {
  description = "The name of the target GKE cluster to deploy the controller."
  type        = string
}

variable "cluster_location" {
  description = "The location (region or zone) of the target GKE cluster."
  type        = string
  default     = "us-central1-c"
}

variable "repository_id" {
  description = "The Artifact Registry repository to create. Only used when build_from_source = true."
  type        = string
  default     = "aibom-repo"
}

variable "namespace" {
  description = "The Kubernetes namespace to install the k8s-aibom controller."
  type        = string
  default     = "k8s-aibom-system"
}

variable "chart_version" {
  description = "Version of the published k8s-aibom Helm chart to install (oci://ghcr.io/googlecloudplatform/charts/k8s-aibom). The published chart pins the controller image by digest and ships provenance and SBOM attestations."
  type        = string
  default     = "1.6.2"
}

variable "build_from_source" {
  description = "Air-gap / development path: build the controller image from this checkout via Cloud Build and deploy the local chart with the image overridden. Defaults to false — the default install uses the published, signed, digest-pinned artifact and requires no Cloud Build or Artifact Registry. See docs/building-from-source.md."
  type        = bool
  default     = false
}

variable "image_tag" {
  description = "Image tag for the locally built container. Only used (and required) when build_from_source = true."
  type        = string
  default     = ""
}
