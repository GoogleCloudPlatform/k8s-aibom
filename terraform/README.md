# k8s-aibom Terraform Automation

This Terraform module fully automates the "Bring Your Own Image" (BYOI) deployment path for `k8s-aibom` on Google Cloud Platform. 

Because `k8s-aibom` does not publish a pre-built container image to public registries, this module will automatically:
1. Provision a Google Artifact Registry repository in your project.
2. Utilize Cloud Build (`gcloud builds submit`) to natively compile the controller image on AMD64 infrastructure.
3. Deploy the `k8s-aibom` Helm chart to your GKE cluster, dynamically injecting the new image URL.

> [!WARNING]
> **API and Billing Requirements:** This module requires the `cloudbuild.googleapis.com` and `artifactregistry.googleapis.com` APIs to be enabled on your GCP project. Utilizing Cloud Build and Artifact Registry incurs standard GCP billing costs.

## Prerequisites
- Terraform >= 1.0
- Google Cloud SDK (`gcloud`) installed and authenticated.
- A running Google Kubernetes Engine (GKE) cluster.

## Quickstart

1. Initialize Terraform:
   ```bash
   terraform init
   ```

2. Plan the deployment. You will be prompted to enter your GCP `project_id` and target GKE `cluster_name`:
   ```bash
   terraform plan
   ```

3. Apply the infrastructure:
   ```bash
   terraform apply
   ```

## Variables

| Name | Description | Default |
|------|-------------|---------|
| `project_id` | Your GCP Project ID | **Required** |
| `cluster_name` | Target GKE cluster name | **Required** |
| `region` | GCP region for Artifact Registry | `us-central1` |
| `cluster_location` | Region or zone of your GKE cluster | `us-central1-c` |
| `repository_id` | Artifact Registry repository name | `aibom-repo` |
| `namespace` | Target deployment namespace | `k8s-aibom-system` |
| `image_tag` | Tag for the built image | `latest` |
