#!/usr/bin/env bash
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

# Non-default-configuration e2e matrix (issue #59). Runs against a kind
# cluster where the chart is already installed and Available (the base
# e2e job's end state). Three legs, each exercising a configuration the
# default install never touches:
#
#   A. strict configuration readiness — break/recover
#   B. sinks under real RBAC (bearer-token Secret, sinkSecretAccess)
#   C. signature verification — verified and tampered outcomes,
#      staticBundle trust root via the new extraVolumes support
#
# Requirements: kubectl + helm on PATH, cwd = repo root, image already
# loaded into the cluster (base job), release name k8s-aibom in
# k8s-aibom-system, chart at ./charts/k8s-aibom.
set -euo pipefail

NS_SYS=k8s-aibom-system
NS=matrix-e2e
RELEASE=k8s-aibom
CHART=./charts/k8s-aibom
HELM_BASE_ARGS=(--namespace "$NS_SYS" --reuse-values)

log()  { echo ">>> $(date -u +%H:%M:%S) $*"; }
fail() { echo "FAIL: $*" >&2; kubectl -n "$NS_SYS" logs deploy/$RELEASE --tail=40 >&2 || true; exit 1; }

wait_for() { # wait_for <seconds> <description> <command...>
  local t=$1 desc=$2; shift 2
  for _ in $(seq 1 "$t"); do
    if "$@" >/dev/null 2>&1; then log "ok: $desc"; return 0; fi
    sleep 1
  done
  fail "timeout waiting for: $desc"
}

pod_ready() {
  [ "$(kubectl -n "$NS_SYS" get pods -l app.kubernetes.io/name=k8s-aibom \
      -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}')" = "$1" ]
}

kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label ns "$NS" aibom.k8saibom.dev/enabled=true --overwrite >/dev/null

# ---------------------------------------------------------------------------
log "LEG A: strict configuration readiness (break / recover)"
# ---------------------------------------------------------------------------
helm upgrade "$RELEASE" "$CHART" "${HELM_BASE_ARGS[@]}" \
  --set readiness.strictConfig=true --wait --timeout 2m >/dev/null
wait_for 60 "pod Ready under strict mode with valid config" pod_ready True

# Invalid config: a pattern that does not compile. All-or-nothing load
# rule → configInvalid → strict readiness fails the probe.
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: aibom.k8saibom.dev/v1beta1
kind: AIBOMControllerConfig
metadata:
  name: default
spec:
  discovery:
    inferenceRuntimeImagePatterns:
      - runtime: broken
        pattern: "(unclosed"
EOF
wait_for 120 "pod NotReady on invalid config (strict)" pod_ready False

# Recover: restore a valid (empty-override) config.
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: aibom.k8saibom.dev/v1beta1
kind: AIBOMControllerConfig
metadata:
  name: default
spec: {}
EOF
wait_for 120 "pod Ready after config fix" pod_ready True
log "LEG A passed"

# ---------------------------------------------------------------------------
log "LEG B: webhook sink with bearer-token Secret under real RBAC"
# ---------------------------------------------------------------------------
kubectl -n "$NS_SYS" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: sink-token
stringData:
  token: matrix-e2e-token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-sink
spec:
  replicas: 1
  selector: {matchLabels: {app: echo-sink}}
  template:
    metadata: {labels: {app: echo-sink}}
    spec:
      containers:
        - name: echo
          image: mendhak/http-https-echo:31
          ports: [{containerPort: 8080}]
---
apiVersion: v1
kind: Service
metadata:
  name: echo-sink
spec:
  selector: {app: echo-sink}
  ports: [{port: 80, targetPort: 8080}]
EOF
kubectl -n "$NS_SYS" rollout status deploy/echo-sink --timeout=120s >/dev/null

helm upgrade "$RELEASE" "$CHART" "${HELM_BASE_ARGS[@]}" \
  --set rbac.sinkSecretAccess=true \
  --set 'config.sinks[0].name=echo' \
  --set 'config.sinks[0].type=Webhook' \
  --set 'config.sinks[0].webhook.endpoint=http://echo-sink.k8s-aibom-system.svc/bom' \
  --set 'config.sinks[0].webhook.auth.bearerToken.secretRef.name=sink-token' \
  --set 'config.sinks[0].webhook.auth.bearerToken.secretRef.key=token' \
  --wait --timeout 2m >/dev/null
wait_for 60 "pod Ready with sink configured" pod_ready True

kubectl -n "$NS" apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: sinkcheck-vllm}
spec:
  replicas: 0
  selector: {matchLabels: {app: sinkcheck-vllm}}
  template:
    metadata: {labels: {app: sinkcheck-vllm}}
    spec:
      containers:
        - name: vllm
          image: vllm/vllm-openai:v0.6.3
          args: ["--model", "facebook/opt-125m"]
EOF
aibom_cond() { kubectl -n "$NS" get aibom "$1" -o jsonpath="{.status.conditions[?(@.type==\"$2\")].status}" 2>/dev/null; }
wait_for 90 "AIBOM Ready with sink fan-out" \
  bash -c '[ "$(kubectl -n matrix-e2e get aibom apps-deployment-sinkcheck-vllm -o jsonpath="{.status.conditions[?(@.type==\"Ready\")].status}" 2>/dev/null)" = "True" ]'
# SinkFailed is set on the sink fan-out pass, which can land after
# Ready; wait for it to become False rather than asserting instantly.
wait_for 90 "SinkFailed=False under RBAC-gated Secret sink" \
  bash -c '[ "$(kubectl -n matrix-e2e get aibom apps-deployment-sinkcheck-vllm -o jsonpath="{.status.conditions[?(@.type==\"SinkFailed\")].status}" 2>/dev/null)" = "False" ]' || {
  kubectl -n "$NS" get aibom apps-deployment-sinkcheck-vllm -o jsonpath='{.status.conditions}' >&2 || true
  fail "SinkFailed never reached False"
}
if kubectl -n "$NS_SYS" logs deploy/$RELEASE --tail=200 | grep -qi "secrets .* forbidden"; then
  fail "forbidden Secret access in controller logs"
fi
log "LEG B passed"

# ---------------------------------------------------------------------------
log "LEG C: signature verification (staticBundle trust root; verified + tampered)"
# ---------------------------------------------------------------------------
kubectl -n "$NS_SYS" create configmap trust-root \
  --from-file=trusted-root.json=verifier/testdata/trusted-root-public-good.json \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

helm upgrade "$RELEASE" "$CHART" "${HELM_BASE_ARGS[@]}" \
  --set 'config.verification.enabled=true' \
  --set 'config.verification.trustRootMode=staticBundle' \
  --set 'config.verification.staticBundlePath=/etc/aibom-trust/trusted-root.json' \
  --set 'extraVolumes[0].name=trust-root' \
  --set 'extraVolumes[0].configMap.name=trust-root' \
  --set 'extraVolumeMounts[0].name=trust-root' \
  --set 'extraVolumeMounts[0].mountPath=/etc/aibom-trust' \
  --set 'extraVolumeMounts[0].readOnly=true' \
  --wait --timeout 2m >/dev/null
wait_for 60 "pod Ready with verification enabled" pod_ready True

BUNDLE_B64=$(base64 < verifier/testdata/bundle-provenance.json | tr -d '\n')
# Tamper INSIDE the DSSE payload's base64 so JSON stays valid but the
# signature check fails.
TAMPERED_B64=$(python3 - <<PY
import base64, json
raw = open("verifier/testdata/bundle-provenance.json","rb").read()
i = raw.index(b'"payload"') + 30
raw = raw[:i] + (b"B" if raw[i:i+1] == b"A" else b"A") + raw[i+1:]
print(base64.b64encode(raw).decode())
PY
)

for name in goodsig badsig; do
  B64=$BUNDLE_B64; [ "$name" = badsig ] && B64=$TAMPERED_B64
  kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $name
  annotations:
    model.k8saibom.dev/name: "pkg:npm/sigstore@1.3.0"
    model.k8saibom.dev/oms-signature: "base64:$B64"
spec:
  replicas: 0
  selector: {matchLabels: {app: $name}}
  template:
    metadata: {labels: {app: $name}}
    spec:
      containers:
        - name: vllm
          image: vllm/vllm-openai:v0.6.3
EOF
done

signed_state() {
  kubectl -n "$NS" get aibom "apps-deployment-$1" \
    -o jsonpath='{.status.summary.models[0].signed}' 2>/dev/null
}
wait_for 120 "good signature reaches verified" bash -c '[ "$(kubectl -n matrix-e2e get aibom apps-deployment-goodsig -o jsonpath="{.status.summary.models[0].signed}" 2>/dev/null)" = "verified" ]'
wait_for 120 "tampered signature stays claimed" bash -c '[ "$(kubectl -n matrix-e2e get aibom apps-deployment-badsig -o jsonpath="{.status.summary.models[0].signed}" 2>/dev/null)" = "claimed" ]'
log "LEG C passed"

log "e2e matrix: ALL LEGS PASSED"
