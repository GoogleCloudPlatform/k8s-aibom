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

# Release gate for the verifier module pin (#397).
#
# The root go.mod requires the nested verifier module by version (no
# replace directive), so a release could ship a verifier different
# from the ./verifier tree in the tagged source in two ways: the
# required version points at a commit whose verifier/ differs from
# HEAD, or the module the proxy serves for that version differs from
# the tree. This script fails on either, before anything is
# published. Two checks:
#
#   1. Pin check: resolve the required version to a commit (a
#      pseudo-version embeds its commit; a vX.Y.Z require resolves
#      via the verifier/vX.Y.Z tag) and require that commit's
#      verifier/ tree to equal HEAD's.
#   2. Proxy check: `go mod download -json` the required module and
#      compare its h1: zip hash (dirhash.Hash1 over the module files)
#      with the same hash computed over the local tree. The module zip
#      also pulls in the LICENSE file from the repo root, so the
#      local hash covers ./verifier plus that file.
#
# Run by the release workflow before any artifact is built, and by the
# release manager from docs/release-checklist.md after tagging
# verifier/vX.Y.Z and bumping the require.
#
# This is NOT a per-PR check: between releases the require
# legitimately lags HEAD, so a strict pin-equals-tree check would fail
# on any PR that touches verifier/. In-tree drift between tags is
# covered on every PR by the go.work step in
# .github/workflows/test.yaml instead.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

MODULE="github.com/GoogleCloudPlatform/k8s-aibom/verifier"

fail() { echo "check-verifier-pin: FAIL: $*" >&2; exit 1; }

# hash1_dir <dir> <prefix>: the dirhash.Hash1 digest of a directory
# ("h1:" + base64 of the sha256 over "<hex>  <prefixed-name>" lines,
# files sorted bytewise). Matches `go mod download -json`'s Sum for a
# module whose file set is <dir>.
hash1_dir() {
  local dir="$1" prefix="$2"
  ( cd "$dir" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
      printf '%s  %s\n' "$(sha256sum "$f" | cut -d' ' -f1)" "${prefix}${f}"
    done ) | openssl dgst -sha256 -binary | base64 | tr -d '\n' | sed 's/^/h1:/'
  echo
}

REQ="$(go list -m -f '{{.Version}}' "$MODULE")"
[ -n "$REQ" ] || fail "could not read the required $MODULE version from go.mod"

# 1. Pin check: resolve the required version to a commit.
case "$REQ" in
  v0.0.0-??????????????-????????????)
    # Pseudo-version: v0.0.0-<date>-<12-hex commit>.
    PIN_COMMIT="${REQ##*-}" ;;
  v[0-9]*.[0-9]*.[0-9]*)
    PIN_COMMIT="$(git rev-parse "verifier/${REQ}^{commit}" 2>/dev/null)" \
      || fail "tag verifier/${REQ} does not exist; tag it before releasing (docs/release-checklist.md)" ;;
  *) fail "cannot resolve required verifier version ${REQ} to a commit" ;;
esac

git cat-file -e "${PIN_COMMIT}^{commit}" 2>/dev/null \
  || fail "pinned commit ${PIN_COMMIT} is not in this clone; fetch full history (git fetch --unshallow) and retry"
git diff --quiet "$PIN_COMMIT" HEAD -- verifier/ \
  || fail "required verifier ${REQ} (commit ${PIN_COMMIT}) differs from ./verifier at HEAD; tag verifier/vX.Y.Z on this commit and bump the require (docs/release-checklist.md)"
echo "check-verifier-pin: pin OK: ${REQ} is the content of ./verifier at HEAD"

# 2. Proxy check: the module the proxy serves must be the same tree.
DL_JSON="$(go mod download -json "$MODULE")"
DL_SUM="$(printf '%s' "$DL_JSON" | grep '"Sum"' | sed 's/.*: *"//; s/",\? *$//')"
DL_DIR="$(printf '%s' "$DL_JSON" | grep '"Dir"' | sed 's/.*: *"//; s/",\? *$//')"
DL_VERSION="$(printf '%s' "$DL_JSON" | grep '"Version"' | sed 's/.*: *"//; s/",\? *$//')"
[ "$DL_VERSION" = "$REQ" ] || fail "proxy serves ${MODULE}@${DL_VERSION}, not the required ${REQ}"
[ -n "$DL_SUM" ] && [ -n "$DL_DIR" ] || fail "go mod download -json returned no Sum/Dir for ${MODULE}@${REQ}"
[ -d "$DL_DIR" ] || fail "downloaded module dir ${DL_DIR} is missing"

# Every file the proxy ships must have a counterpart in this tree:
# under ./verifier/, else at the repo root (LICENSE and friends).
PREFIX="${MODULE}@${REQ}/"
while IFS= read -r rel; do
  if [ -f "verifier/$rel" ]; then :; elif [ -f "$rel" ]; then :; else
    fail "module file ${rel} has no counterpart in this tree"
  fi
done < <(cd "$DL_DIR" && find . -type f | sed 's|^\./||' | LC_ALL=C sort)

# The local tree must not carry a file the pinned module does not
# ship. Module zips drop files whose name starts with '.' or '_'
# (any path element), so those are excluded here.
while IFS= read -r rel; do
  [ -f "$DL_DIR/$rel" ] || fail "./verifier/${rel} is not in ${MODULE}@${REQ}; the pin is stale"
done < <(cd verifier && find . -type f | sed 's|^\./||' | grep -v -E '(^|/)[._]' | LC_ALL=C sort)

# Same hash, same content: stage the local counterparts of the
# downloaded file set in the module's layout, hash the stage, and
# compare with the zip hash the proxy reports.
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
while IFS= read -r rel; do
  if [ -f "verifier/$rel" ]; then src="verifier/$rel"; else src="$rel"; fi
  mkdir -p "$STAGE/$(dirname "$rel")"
  cp "$src" "$STAGE/$rel"
done < <(cd "$DL_DIR" && find . -type f | sed 's|^\./||' | LC_ALL=C sort)
LOCAL_HASH="$(hash1_dir "$STAGE" "$PREFIX")"
[ "$LOCAL_HASH" = "$DL_SUM" ] \
  || fail "module ${MODULE}@${REQ} hashes ${DL_SUM} but the local tree hashes ${LOCAL_HASH}"
echo "check-verifier-pin: proxy OK: ${MODULE}@${REQ} matches the local tree (${DL_SUM})"
