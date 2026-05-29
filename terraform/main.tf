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

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.12"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# Fetch the GKE cluster credentials dynamically
data "google_client_config" "default" {}

data "google_container_cluster" "target" {
  name     = var.cluster_name
  location = var.cluster_location
  project  = var.project_id
}

provider "helm" {
  kubernetes {
    host                   = "https://${data.google_container_cluster.target.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(data.google_container_cluster.target.master_auth.0.cluster_ca_certificate)
  }
}

# 1. Provision the Artifact Registry
resource "google_artifact_registry_repository" "aibom_repo" {
  location      = var.region
  repository_id = var.repository_id
  description   = "k8s-aibom controller image repository"
  format        = "DOCKER"
  project       = var.project_id
}

# 2. Build and push the image via Cloud Build (AMD64 natively)
resource "null_resource" "build_image" {
  triggers = {
    # Rebuild if the Dockerfile, Makefile, or image tag changes
    dockerfile_sha = filesha256("${path.module}/../Dockerfile")
    makefile_sha   = filesha256("${path.module}/../Makefile")
    image_tag      = var.image_tag
  }

  depends_on = [
    google_artifact_registry_repository.aibom_repo
  ]

  provisioner "local-exec" {
    command = <<EOT
      gcloud builds submit ${path.module}/../ \
        --project ${var.project_id} \
        --tag ${var.region}-docker.pkg.dev/${var.project_id}/${var.repository_id}/k8s-aibom:${var.image_tag}
    EOT
  }
}

# 3. Deploy the Helm chart, overriding the image registry
resource "helm_release" "k8s_aibom" {
  name             = "k8s-aibom"
  chart            = "${path.module}/../charts/k8s-aibom"
  namespace        = var.namespace
  create_namespace = true

  set {
    name  = "image.repository"
    value = "${var.region}-docker.pkg.dev/${var.project_id}/${var.repository_id}/k8s-aibom"
  }

  set {
    name  = "image.tag"
    value = var.image_tag
  }

  # Ensure the image is built and pushed before Helm tries to pull it
  depends_on = [null_resource.build_image]
}
