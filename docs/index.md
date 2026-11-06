---
title: k8s-aibom
---

# k8s-aibom

Know what AI is actually running in your clusters. k8s-aibom is an unprivileged Kubernetes controller that inventories models, runtimes, agents and RAG components at runtime and writes one CycloneDX 1.6 ML-BOM per workload, with evidence and a confidence tier on every attribute.

- **Source, install and quickstart:** [github.com/GoogleCloudPlatform/k8s-aibom](https://github.com/GoogleCloudPlatform/k8s-aibom)
- **Releases:** [github.com/GoogleCloudPlatform/k8s-aibom/releases](https://github.com/GoogleCloudPlatform/k8s-aibom/releases)
- **Overview deck:** [deck/](deck/)

## Using it

- [Install on EKS, AKS and other conformant clusters](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/install-eks-aks.md)
- [Building from source](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/building-from-source.md)
- [Compatibility](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/compatibility.md)
- [Migration to v1beta1](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/migration-v1beta1.md)
- [Incident response with `kubectl aibom`](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/incident-response.md)
- [Policy cookbook (Kyverno, Gatekeeper)](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/policy-cookbook.md)
- [Dependency-Track integration](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/integrations/dependency-track.md)
- [Webhook sink protocol](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/webhook-sink-protocol.md)

## How it decides

- [Scraper heuristics](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/scraper-heuristics.md)
- [Contributing a detection pattern](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/contributing-a-pattern.md)
- [CycloneDX property taxonomy](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/cyclonedx-property-taxonomy.md)
- [Schema divergences](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/schema-divergences.md)
- [External CRD versions](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/external-crd-versions.md)

## Trust

- [Security model](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/security-model.md)
- [Threat model](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/threat-model.md)
- [Quality baseline](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/quality-baseline.md)
- [Evidence notes](https://github.com/GoogleCloudPlatform/k8s-aibom/tree/main/docs/evidence) from real-cluster validation and downstream requalification

## Project

- [Design documents](https://github.com/GoogleCloudPlatform/k8s-aibom/tree/main/docs/design)
- [Roadmap](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/roadmap.md)
- [Merge criteria](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/merge-criteria.md)
- [Release checklist](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/docs/release-checklist.md)
- [Adopters](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/ADOPTERS.md)
