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

# Exercises verify-changelog-released.sh in a throwaway repository.
set -euo pipefail
script="$(cd "$(dirname "$0")" && pwd)/verify-changelog-released.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q .
git config user.name test
git config user.email test@example.invalid
fail() { echo "FAIL: $1" >&2; exit 1; }

printf '# Changelog\n\n## [Unreleased]\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n\n- one\n' > CHANGELOG.md
git add CHANGELOG.md && git commit -q -m release && git tag v1.0.0
git tag v1.1.0-rc.1   # a pre-release tag must be ignored

# 1. unchanged file passes
"$script" >/dev/null || fail "an unchanged changelog must pass"

# 2. an entry under [Unreleased] passes
printf '# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- new\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n\n- one\n' > CHANGELOG.md
"$script" >/dev/null || fail "an entry under [Unreleased] must pass"

# 3. an entry inside the released section fails
printf '# Changelog\n\n## [Unreleased]\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n\n- stray\n- one\n' > CHANGELOG.md
if "$script" >/dev/null 2>&1; then fail "an entry inside a released section must fail"; fi

# 4. a new release section above the old one passes (release preparation)
printf '# Changelog\n\n## [Unreleased]\n\n## [1.1.0] - 2026-02-01\n\n### Fixed\n\n- new\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n\n- one\n' > CHANGELOG.md
"$script" >/dev/null || fail "a new release section above the last release must pass"

echo "verify-changelog-released: 4 cases passed"
