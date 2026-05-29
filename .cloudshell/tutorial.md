# k8s-aibom Cloud Shell Tutorial

<walkthrough-tutorial-duration>15</walkthrough-tutorial-duration>
<walkthrough-tutorial-difficulty>Beginner</walkthrough-tutorial-difficulty>

## Project Setup

Select the Google Cloud project you want to use. This project will host your Artifact Registry and Google Kubernetes Engine (GKE) cluster.

<walkthrough-project-setup></walkthrough-project-setup>

## Enable APIs

You will need the Artifact Registry API and the Google Kubernetes Engine (GKE) API enabled in your project.

```bash
gcloud services enable artifactregistry.googleapis.com container.googleapis.com
```

## Configure Authentication and Region

Configure Docker to authenticate with Artifact Registry. Because Cloud Shell is ephemeral, we do this for the current session.

```bash
gcloud auth configure-docker us-central1-docker.pkg.dev
```

Set the default compute region:

```bash
gcloud config set compute/region us-central1
```

## Create a Cluster and Registry

Create a GKE Autopilot cluster for testing (Note: This will take ~5-10 minutes to provision):

```bash
gcloud container clusters create-auto aibom-demo --region us-central1
```

Create an Artifact Registry repository to host your container image:

```bash
gcloud artifacts repositories create aibom-repo \
  --repository-format=docker \
  --location=us-central1
```

## Build and Push the Controller Image

Build the `k8s-aibom` image locally in Cloud Shell using Docker, and push it to your Artifact Registry. We define the `IMG` environment variable so the `Makefile` pushes to the correct registry location.

```bash
export PROJECT_ID=$(gcloud config get-value project)
export IMG=us-central1-docker.pkg.dev/${PROJECT_ID}/aibom-repo/k8s-aibom:v1.0.0

make image
make docker-push
```

## Deploy with Helm

Deploy the controller to your cluster using Helm, injecting your registry path and tag:

```bash
helm install k8s-aibom ./charts/k8s-aibom \
  --namespace k8s-aibom-system \
  --create-namespace \
  --set image.repository=us-central1-docker.pkg.dev/${PROJECT_ID}/aibom-repo/k8s-aibom \
  --set image.tag=v1.0.0
```

## Verify Installation

The controller is installed cluster-wide but remains inactive until you opt in at least one namespace. Let's opt in the `default` namespace:

```bash
kubectl label namespace default aibom.k8saibom.dev/enabled=true
```

Verify the controller pod is running:

```bash
kubectl get pods -n k8s-aibom-system
```

<walkthrough-conclusion-trophy></walkthrough-conclusion-trophy>

## Conclusion

You have successfully deployed k8s-aibom using the Bring Your Own Image (BYOI) strategy!
You can now deploy AI workloads to the `default` namespace and observe the generated AIBOM resources.
