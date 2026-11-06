---
title: Overview
nav_order: 1
---

# k8s-aibom

Know what AI is actually running in your clusters. k8s-aibom is an unprivileged Kubernetes controller that inventories models, runtimes, agents and RAG components at runtime and writes one CycloneDX 1.6 ML-BOM per workload, with evidence and a confidence tier on every attribute.

- **Source, install and quickstart:** [github.com/GoogleCloudPlatform/k8s-aibom](https://github.com/GoogleCloudPlatform/k8s-aibom)
- **Releases:** [github.com/GoogleCloudPlatform/k8s-aibom/releases](https://github.com/GoogleCloudPlatform/k8s-aibom/releases)
- **Overview deck:** [deck/](deck/)

## Using it

- [Install on EKS, AKS and other conformant clusters](install-eks-aks.md)
- [Building from source](building-from-source.md)
- [Compatibility](compatibility.md)
- [Migration to v1beta1](migration-v1beta1.md)
- [Incident response with `kubectl aibom`](incident-response.md)
- [Policy cookbook (Kyverno, Gatekeeper)](policy-cookbook.md)
- [Dependency-Track integration](integrations/dependency-track.md)
- [Webhook sink protocol](webhook-sink-protocol.md)

## How it decides

- [Scraper heuristics](scraper-heuristics.md)
- [Contributing a detection pattern](contributing-a-pattern.md)
- [CycloneDX property taxonomy](cyclonedx-property-taxonomy.md)
- [Schema divergences](schema-divergences.md)
- [External CRD versions](external-crd-versions.md)

## Trust

- [Security model](security-model.md)
- [Threat model](threat-model.md)
- [Quality baseline](quality-baseline.md)
- [Evidence notes](https://github.com/GoogleCloudPlatform/k8s-aibom/tree/main/docs/evidence) from real-cluster validation and downstream requalification

## Project

- [Design documents](https://github.com/GoogleCloudPlatform/k8s-aibom/tree/main/docs/design)
- [Roadmap](roadmap.md)
- [Merge criteria](merge-criteria.md)
- [Release checklist](release-checklist.md)
- [Adopters](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/ADOPTERS.md)
