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

# Released changelog sections are a record: once a version is tagged, its
# section and everything below it must not change. A pull request written
# before a release and merged after it keeps its entry at the old position,
# which is now inside the released section, and git merges that without a
# conflict (this happened to #418 after v1.7.0). This check fails when the
# file from "## [<latest release>]" down differs from the same span of
# CHANGELOG.md at that release's tag.
#
# Usage: hack/verify-changelog-released.sh [changelog] (default CHANGELOG.md)
# Needs the release tags in the clone (git fetch --tags).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
file="${1:-CHANGELOG.md}"

# Latest final release tag (vX.Y.Z, no pre-release suffix), by version order.
tag="$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -v -- '-' | sort -V | tail -1 || true)"
if [ -z "$tag" ]; then
  echo "verify-changelog-released: no release tag in this clone; run 'git fetch --tags'" >&2
  exit 1
fi
version="${tag#v}"
heading="## [${version}]"

span() { awk -v h="$heading" 'index($0, h) == 1 {on = 1} on {print}'; }

want="$(git show "${tag}:CHANGELOG.md" | span)"
got="$(span < "$file")"
if [ -z "$want" ]; then
  echo "verify-changelog-released: ${tag}:CHANGELOG.md has no '${heading}' section" >&2
  exit 1
fi
if [ "$want" != "$got" ]; then
  echo "verify-changelog-released: FAIL: ${file} differs from ${tag} from '${heading}' down." >&2
  echo "An entry probably landed inside a released section; move it under [Unreleased]." >&2
  diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | head -20 >&2 || true
  exit 1
fi
echo "verify-changelog-released: OK: ${file} matches ${tag} from '${heading}' down"
