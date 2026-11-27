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

# Exercises check-verifier-pin.sh against a fixture git repo with a
# stub go: no module proxy, no network. Run by the release dry-run
# workflow so the release-time pin gate cannot rot between releases.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
module="github.com/GoogleCloudPlatform/k8s-aibom/verifier"

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

sha256_hex() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  else openssl dgst -sha256 "$1" | sed 's/^.*= //'; fi
}

# hash1 <dir> <prefix>: dirhash.Hash1 of a directory, as in the script.
hash1() {
  ( cd "$1" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
      printf '%s  %s\n' "$(sha256_hex "$f")" "$2$f"
    done ) | openssl dgst -sha256 -binary | base64 | tr -d '\n' | sed 's/^/h1:/'
}

# Fixture repo: verifier/ module plus the root LICENSE the module zip
# pulls in, committed once; the pin is a pseudo-version of that commit.
repo="$work/repo"
mkdir -p "$repo/verifier"
printf 'module %s\n\ngo 1.24\n' "$module" >"$repo/verifier/go.mod"
echo 'package verifier' >"$repo/verifier/verifier.go"
echo 'fixture license' >"$repo/LICENSE"
git -C "$repo" init -q
git -C "$repo" add -A
git -C "$repo" commit -qm fixture
version="v0.0.0-20260101000000-$(git -C "$repo" rev-parse HEAD | cut -c1-12)"

# The "downloaded" module: the verifier files plus LICENSE, laid out
# as the proxy zip ships them, hashed with the module prefix.
dl="$work/dl"
mkdir -p "$dl"
cp "$repo/verifier/go.mod" "$repo/verifier/verifier.go" "$repo/LICENSE" "$dl/"
sum="$(hash1 "$dl" "${module}@${version}/")"

# stub go: reports the pinned version and the staged download.
mkdir -p "$work/bin"
cat >"$work/bin/go" <<'STUB'
#!/usr/bin/env bash
if [[ "$1" == "list" ]]; then echo "$GO_STUB_VERSION"; exit 0; fi
if [[ "$1" == "mod" && "$2" == "download" ]]; then
  printf '{\n  "Version": "%s",\n  "Dir": "%s",\n  "Sum": "%s"\n}\n' \
    "$GO_STUB_VERSION" "$GO_STUB_DIR" "$GO_STUB_SUM"
  exit 0
fi
echo "stub go: unexpected arguments: $*" >&2; exit 1
STUB
chmod +x "$work/bin/go"

run() {
  ( cd "$repo" && PATH="$work/bin:$PATH" GO_STUB_VERSION="$version" \
      GO_STUB_DIR="$dl" GO_STUB_SUM="$sum" \
      bash "$here/check-verifier-pin.sh" ) 2>"$work/stderr"
}
fail() { echo "FAIL: $1" >&2; cat "$work/stderr" >&2; exit 1; }

# 1. pin and proxy agree with the tree: passes, both checks report OK
out=$(run) || fail "matching pin and proxy should pass"
[[ "$out" == *"pin OK"* && "$out" == *"proxy OK"* ]] || fail "expected pin OK and proxy OK, got: $out"

# 2. verifier file tampered in the working tree: the staged local
#    hash no longer matches the downloaded module sum
echo 'tampered' >>"$repo/verifier/verifier.go"
if run >/dev/null; then fail "tampered verifier file must fail the proxy check"; fi
git -C "$repo" checkout -- verifier/verifier.go

# 3. verifier/ committed after the pinned commit: the pin check fails
echo '// changed after the pin' >>"$repo/verifier/verifier.go"
git -C "$repo" commit -qam 'change verifier after pinning'
if run >/dev/null; then fail "verifier changed after the pin must fail the pin check"; fi

echo "check-verifier-pin: 3 cases passed"
