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
  description = "The name of the Artifact Registry repository to create."
  type        = string
  default     = "aibom-repo"
}

variable "namespace" {
  description = "The Kubernetes namespace to install the k8s-aibom controller."
  type        = string
  default     = "k8s-aibom-system"
}

variable "image_tag" {
  description = "The image tag to apply to the built container."
  type        = string
}
