# Policy Cookbook: acting on AIBOM facts

k8s-aibom emits facts, never judgments — that boundary is a design
rule. This cookbook shows where the judgments go: policy engines that
consume the facts. Every example below is **audit/advisory mode**;
enforce only after you've watched the audit results in your own
clusters.

## 1. "AI runtimes must be inventoried" (Kyverno)

Namespaces running recognizable AI-serving images should carry the
opt-in label — otherwise those workloads are invisible to inventory.
This audit surfaces the gap without touching the workload:

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: ai-workloads-should-be-inventoried
spec:
  validationFailureAction: Audit
  background: true
  rules:
    - name: known-ai-runtime-needs-optin
      match:
        any:
          - resources:
              kinds: [Deployment, StatefulSet]
      preconditions:
        all:
          # Matches the common serving-runtime image families the
          # controller detects; extend to taste.
          - key: "{{ request.object.spec.template.spec.containers[?contains(image, 'vllm') || contains(image, 'triton') || contains(image, 'text-generation-inference') || contains(image, 'nvcr.io/nim/')] | length(@) }}"
            operator: GreaterThan
            value: 0
      context:
        - name: nsLabels
          apiCall:
            urlPath: "/api/v1/namespaces/{{ request.namespace }}"
            jmesPath: "metadata.labels"
      validate:
        message: >-
          Namespace {{ request.namespace }} runs an AI serving runtime
          but is not opted in to runtime inventory. Label it:
          kubectl label ns {{ request.namespace }} aibom.k8saibom.dev/enabled=true
        deny:
          conditions:
            all:
              - key: "{{ nsLabels.\"aibom.k8saibom.dev/enabled\" || '' }}"
                operator: NotEquals
                value: "true"
```

## 2. "Production models should be verified" (Kyverno, over AIBOM CRs)

With `spec.verification` enabled (v1.5.0+), each model summary carries
`signed: unsigned|claimed|verified`. Audit AIBOMs in production
namespaces whose models aren't verified:

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: prod-models-should-be-verified
spec:
  validationFailureAction: Audit
  background: true
  rules:
    - name: unverified-model-in-prod
      match:
        any:
          - resources:
              kinds: [AIBOM]
              namespaceSelector:
                matchLabels:
                  environment: production
      validate:
        message: >-
          {{ request.object.metadata.name }} serves a model whose
          signature is not verified (see status.summary.models[].signed
          and the signature.* properties in the BOM document).
        deny:
          conditions:
            any:
              - key: "{{ request.object.status.summary.models[?signed != 'verified'] | length(@) }}"
                operator: GreaterThan
                value: 0
```

Advisory by design: an unverified model may be perfectly legitimate
(unsigned upstream weights). The policy's job is to make that a
conscious, visible state — not to block serving.

## 3. Gatekeeper variant of recipe 1

```yaml
apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: aibomoptinrequired
spec:
  crd:
    spec:
      names:
        kind: AIBOMOptInRequired
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package aibomoptin
        ai_image(img) {
          patterns := ["vllm", "triton", "text-generation-inference", "nvcr.io/nim/"]
          some p
          contains(img, patterns[p])
        }
        violation[{"msg": msg}] {
          some c
          ai_image(input.review.object.spec.template.spec.containers[c].image)
          ns := data.inventory.cluster.v1.Namespace[input.review.object.metadata.namespace]
          ns.metadata.labels["aibom.k8saibom.dev/enabled"] != "true"
          msg := sprintf("namespace %v runs an AI runtime without inventory opt-in", [input.review.object.metadata.namespace])
        }
---
apiVersion: constraints.gatekeeper.sh/v1beta1
kind: AIBOMOptInRequired
metadata:
  name: ai-workloads-should-be-inventoried
spec:
  enforcementAction: warn   # audit posture; switch to deny only deliberately
  match:
    kinds:
      - apiGroups: ["apps"]
        kinds: ["Deployment", "StatefulSet"]
```

(Gatekeeper needs `Namespace` in its sync/replication config for the
`data.inventory` lookup.)

## Composition notes

- These compose with, and never replace, image-signature admission
  (sigstore policy-controller, Kyverno verifyImages): those gate
  *container images* at admission; k8s-aibom records *model* facts at
  runtime. Different subjects, complementary controls.
- `kubectl aibom summary` is the human-shaped view of the same facts
  these policies consume; use it to sanity-check a policy before
  enabling it.
- Recipe images-pattern lists here are illustrative. The controller's
  own detection allowlist (`v1-runtime-patterns.yaml`, extensible via
  `spec.discovery`) is the authoritative set; keep policies aligned
  with what you actually configure.
