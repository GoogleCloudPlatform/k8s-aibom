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

# Exercises push-chart.sh against a stub helm: no registry, no network.
# Run by the release dry-run workflow so the release's push path is
# tested on every pull request.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
want="sha256:$(printf 'a%.0s' {1..64})"

# stub helm: fails FAIL_TIMES times with the v1.7.0 registry error, then
# prints what a real push prints. MODE=nodigest succeeds without a digest.
cat >"$work/helm" <<STUB
#!/usr/bin/env bash
count_file="$work/count"
n=\$(cat "\$count_file" 2>/dev/null || echo 0)
n=\$((n + 1)); echo "\$n" >"\$count_file"
if [[ \$n -le \${FAIL_TIMES:-0} ]]; then
  echo 'Error: failed to perform "Tag" on destination: sha256:80eb: not found' >&2
  exit 1
fi
echo "Pushed: example.test/charts/k8s-aibom:9.9.9"
[[ "\${MODE:-}" == nodigest ]] || echo "Digest: $want"
STUB
chmod +x "$work/helm"

run() { rm -f "$work/count"; HELM="$work/helm" PUSH_RETRY_DELAY=0 "$here/push-chart.sh" chart.tgz oci://example.test/charts 2>"$work/stderr"; }
fail() { echo "FAIL: $1" >&2; cat "$work/stderr" >&2; exit 1; }

# 1. clean push: digest on stdout, exactly one line
out=$(FAIL_TIMES=0 run) || fail "clean push should succeed"
[[ "$out" == "$want" ]] || fail "clean push: stdout = '$out'"

# 2. the v1.7.0 failure once, then success: retried, digest returned
out=$(FAIL_TIMES=1 run) || fail "one transient failure should be retried"
[[ "$out" == "$want" ]] || fail "retry: stdout = '$out'"
[[ $(cat "$work/count") == 2 ]] || fail "retry: expected 2 helm calls, got $(cat "$work/count")"

# 3. every attempt fails: non-zero, nothing on stdout
if out=$(FAIL_TIMES=99 PUSH_ATTEMPTS=3 run); then fail "persistent failure must exit non-zero"; fi
[[ -z "$out" ]] || fail "persistent failure: stdout must be empty, got '$out'"
[[ $(cat "$work/count") == 3 ]] || fail "persistent failure: expected 3 attempts, got $(cat "$work/count")"

# 4. helm exits 0 without a digest: non-zero, nothing on stdout
if out=$(MODE=nodigest run); then fail "missing digest must exit non-zero"; fi
[[ -z "$out" ]] || fail "missing digest: stdout must be empty, got '$out'"

# 5. usage error
if "$here/push-chart.sh" only-one-arg 2>/dev/null; then fail "usage error must exit non-zero"; fi

echo "push-chart: 5 cases passed"
