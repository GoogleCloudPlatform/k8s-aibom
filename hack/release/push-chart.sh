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

# push-chart.sh <chart.tgz> <oci-repo>
#
# `helm push` with retries, for the release workflow. Prints the pushed
# manifest digest (sha256:...) on stdout and nothing else; helm's own
# output goes to stderr. Exits non-zero when every attempt fails or when
# helm reports success without a digest, so the step that attests the
# chart can never run with an empty subject.
#
# Why: the v1.7.0 release (2026-11-24) hit a registry error while helm
# applied the version tag ("failed to perform \"Tag\" on destination:
# <digest>: not found"). The push was piped through `tee` without
# pipefail, so the step passed with an empty digest and the failure
# surfaced one step later as a misleading attestation error. A push is
# idempotent (same bytes, same digest), so retrying is safe.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <chart.tgz> <oci-repo>" >&2
  exit 2
fi
chart=$1
repo=$2
helm=${HELM:-helm}
attempts=${PUSH_ATTEMPTS:-4}
delay=${PUSH_RETRY_DELAY:-5}

log=$(mktemp)
trap 'rm -f "$log"' EXIT

for ((i = 1; i <= attempts; i++)); do
  if "$helm" push "$chart" "$repo" >"$log" 2>&1; then
    cat "$log" >&2
    digest=$(awk '/^Digest:/ {print $2}' "$log")
    if [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      echo "$digest"
      exit 0
    fi
    echo "push-chart: helm push reported success but printed no digest" >&2
    exit 1
  fi
  cat "$log" >&2
  echo "push-chart: attempt $i of $attempts failed" >&2
  if ((i < attempts)); then
    sleep $((delay * i))
  fi
done
echo "push-chart: giving up after $attempts attempts" >&2
exit 1
