# syntax=docker/dockerfile:1.6
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


# ---------------------------------------------------------------------------
# Stage 1: build the controller binary.
# ---------------------------------------------------------------------------
#
# golang:1.26-bookworm matches the go directive in go.mod. The Phase 9.5
# smoke test surfaced that `golang:1.24-bookworm` with auto-toolchain
# (Go's GOTOOLCHAIN=auto) fetches Go 1.26 to satisfy go.mod, but some
# CI environments set GOTOOLCHAIN=local which disables the auto-fetch
# and breaks the build. Pinning to 1.26 directly here avoids the
# toolchain-resolution surprise. Bump in lockstep with the go.mod `go`
# directive when upgrading.
FROM golang:1.26-bookworm AS builder

WORKDIR /workspace

# Dependency layer: cached separately from source layers so dependency
# changes don't invalidate the compile cache and vice versa.
COPY go.mod go.sum ./
RUN go mod download

# Source layers
COPY cmd/      cmd/
COPY api/      api/
COPY internal/ internal/

# Cross-compile a fully static binary.
#   - CGO_ENABLED=0: pure-Go, no libc dependency. Required for the
#     distroless:static runtime.
#   - -trimpath: strip absolute filesystem paths from binary, so reproducible
#     builds don't embed the builder's working directory.
#   - -ldflags="-s -w": strip the symbol table and DWARF debug info to
#     shrink the binary (~30% smaller).
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH}
RUN go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /workspace/manager \
    ./cmd/manager

# ---------------------------------------------------------------------------
# Stage 2: runtime image.
# ---------------------------------------------------------------------------
#
# gcr.io/distroless/static-debian12:nonroot — the minimum-surface runtime:
#   - No shell. No package manager. No coreutils. Reduces blast radius
#     if a future vulnerability allows command injection.
#   - Pre-configured nonroot UID/GID 65532. Reinforced by USER below.
#   - Includes only CA certificates and tzdata, both load-bearing for
#     the controller (TLS to GCS / webhook endpoints; UTC timestamps).
#   - Pinned to debian12 to match a specific OS image lineage.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /
COPY --from=builder /workspace/manager /manager

# Explicit non-root user. distroless:nonroot already has USER 65532:65532
# baked in; restating here makes this Dockerfile the self-contained
# source of truth and protects against base-image regressions.
USER 65532:65532

ENTRYPOINT ["/manager"]
