# Quality Baseline: v1.0 Release

This document serves as the public-facing evidence of the project's engineering quality and security baseline. Below is the static-analysis and misconfiguration audit report for the `v1.0` release candidate manifests.

All tools are configured to run automatically in CI on every Pull Request to prevent regressions.

---

## 1. Kube-Linter
**Tool:** `kube-linter` (StackRox)
**Target:** `config/`, `helm/`, `install.yaml`
**Status:** PASS (with documented suppressions)

**Findings Triage:**
- `unset-cpu-requirements`: **Suppressed.** The `k8s-aibom` controller is lightweight, but we do not enforce a hard CPU limit by default to prevent CPU throttling during rapid `AIBOM` generation on large clusters. CPU requests are set.
- `run-as-non-root`: **Suppressed.** The container currently runs as non-root (uid 65532), but the explicit `runAsNonRoot: true` securityContext flag is missing from the default kubebuilder scaffolding. Will be fixed in v1.1.
- `read-only-root-fs`: **Suppressed.** Requires mounting an emptyDir for `/tmp`. Will be fixed in v1.1.

## 2. Kubeconform
**Tool:** `kubeconform`
**Target:** Kubernetes versions `1.27.0` through `1.31.0`
**Status:** PASS 

**Output Snippet:**
```text
Summary: 14 resources found in install.yaml - Valid: 14, Invalid: 0, Errors: 0, Skipped: 0
Summary: 14 resources found in helm-template.yaml - Valid: 14, Invalid: 0, Errors: 0, Skipped: 0
```
All emitted manifests strictly conform to the Kubernetes OpenAPI schemas for the supported version range.

## 3. Kubeaudit
**Tool:** `kubeaudit` (Shopify)
**Target:** `install.yaml`, `helm-template.yaml`
**Status:** PASS (Soft-fail warnings only)

**Findings Triage:**
- `CapabilityAdded`: No privileged capabilities are added.
- `ReadOnlyRootFilesystemFalse`: Flagged as a warning. Addressed via suppression.
- `SeccompProfileMissing`: Flagged as a warning on older Kubernetes versions.

## 4. Conftest (Open Policy Agent)
**Tool:** `conftest` (with `kubernetes-best-practices` policies)
**Target:** `install.yaml`, `helm-template.yaml`
**Status:** PASS

**Output Snippet:**
```text
14 tests, 14 passed, 0 warnings, 0 failures, 0 exceptions
```

## 5. Trivy Config
**Tool:** `trivy config` (Aqua Security)
**Target:** Repository manifests
**Status:** PASS (No HIGH/CRITICAL findings)

**Findings Triage:**
Trivy flags `KSV012` (Read-only file system) and `KSV014` (Root file system) as MEDIUM severity. These do not cross our HIGH/CRITICAL hard-fail threshold and are tracked for remediation in a future PR.

## 6. Helm Lint & Template
**Tool:** `helm`
**Target:** `charts/k8s-aibom`
**Status:** PASS

**Output Snippet:**
```text
==> Linting charts/k8s-aibom
[INFO] Chart.yaml: icon is recommended
1 chart(s) linted, 0 chart(s) failed
```

---
*Note: Any issues surfaced in this report that require source-code fixes are intentionally preserved in the v1.0 codebase and tracked separately to maintain a stable release baseline.*
