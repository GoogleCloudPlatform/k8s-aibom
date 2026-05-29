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

# k8s-aibom Makefile
# Standard kubebuilder-style targets. Tooling (controller-gen, envtest) is
# installed into ./bin so we don't pollute the user's GOPATH.

SHELL := /usr/bin/env bash -o pipefail

# Versions of generated/downloaded tools.
CONTROLLER_GEN_VERSION ?= v0.16.5
ENVTEST_VERSION         ?= release-0.24
ENVTEST_K8S_VERSION     ?= 1.34.0
IMG                     ?= k8s-aibom:latest

LOCALBIN := $(CURDIR)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

CONTROLLER_GEN := $(LOCALBIN)/controller-gen
ENVTEST        := $(LOCALBIN)/setup-envtest

# ---------------------------------------------------------------------------
# Tooling install
# ---------------------------------------------------------------------------

.PHONY: controller-gen
controller-gen: $(LOCALBIN)
	@test -x $(CONTROLLER_GEN) || GOBIN=$(LOCALBIN) go install \
		sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: envtest
envtest: $(LOCALBIN)
	@test -x $(ENVTEST) || GOBIN=$(LOCALBIN) go install \
		sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

# ---------------------------------------------------------------------------
# Code generation
# ---------------------------------------------------------------------------

.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd \
		paths="./api/..." paths="./internal/controller/..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	@for file in config/crd/bases/*.yaml config/rbac/*.yaml; do \
		grep -v "^---$$" $$file > $$file.no-dashes; \
		cat hack/boilerplate.yaml.txt $$file.no-dashes > $$file; \
		rm $$file.no-dashes; \
	done

.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

# ---------------------------------------------------------------------------
# Build / verify
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: build
build: fmt vet
	go build -o $(LOCALBIN)/manager ./cmd/manager

.PHONY: run
run: fmt vet
	go run ./cmd/manager

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

.PHONY: test
test: envtest
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -race -coverprofile=cover.out ./...

.PHONY: cover
cover: test
	go tool cover -html=cover.out -o coverage.html

# ---------------------------------------------------------------------------
# Golden files
# ---------------------------------------------------------------------------

# Regenerate the BOM-builder golden output files in
# internal/bom/testdata/golden/ from the current builder output. Use after
# an intentional BOM-shape change; review the diff in the PR.
.PHONY: update-golden
update-golden:
	go test ./internal/bom/ -run TestGolden -update-golden

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

.PHONY: clean
clean:
	rm -rf $(LOCALBIN) cover.out coverage.html

.PHONY: help
help:
	@echo "k8s-aibom targets:"
	@echo "  manifests       regenerate CRD and RBAC YAML from Go markers"
	@echo "  generate        regenerate DeepCopy methods"
	@echo "  fmt vet         go fmt / go vet"
	@echo "  build           compile ./bin/manager"
	@echo "  run             go run ./cmd/manager"
	@echo "  test            go test ./... (uses envtest)"
	@echo "  cover           HTML coverage report"
	@echo "  clean           remove ./bin and coverage artifacts"

# ---------------------------------------------------------------------------
# Phase 15: Helm and Dockerfile
# ---------------------------------------------------------------------------
HELM := $(LOCALBIN)/helm
.PHONY: helm
helm: $(LOCALBIN)
	@test -x $(HELM) || (curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 && chmod 700 get_helm.sh && ./get_helm.sh --version v3.13.0 --no-sudo && mv $(CURDIR)/bin/helm $(HELM) || rm -f get_helm.sh)

.PHONY: install.yaml
install.yaml: helm
	@echo "# Copyright 2026 Google LLC" > install.yaml
	@echo "#" >> install.yaml
	@echo "# Licensed under the Apache License, Version 2.0 (the \"License\");" >> install.yaml
	@echo "# you may not use this file except in compliance with the License." >> install.yaml
	@echo "# You may obtain a copy of the License at" >> install.yaml
	@echo "#" >> install.yaml
	@echo "#     http://www.apache.org/licenses/LICENSE-2.0" >> install.yaml
	@echo "#" >> install.yaml
	@echo "# Unless required by applicable law or agreed to in writing, software" >> install.yaml
	@echo "# distributed under the License is distributed on an \"AS IS\" BASIS," >> install.yaml
	@echo "# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied." >> install.yaml
	@echo "# See the License for the specific language governing permissions and" >> install.yaml
	@echo "# limitations under the License." >> install.yaml
	@echo "" >> install.yaml
	@echo "---" >> install.yaml
	@echo "apiVersion: v1" >> install.yaml
	@echo "kind: Namespace" >> install.yaml
	@echo "metadata:" >> install.yaml
	@echo "  name: k8s-aibom-system" >> install.yaml
	for file in config/crd/bases/*.yaml; do \
		echo "---" >> install.yaml; \
		grep -v "^---$$" $$file >> install.yaml; \
	done
	$(HELM) template k8s-aibom charts/k8s-aibom -n k8s-aibom-system | sed -e '/helm.sh\/hook/d' >> install.yaml
.PHONY: image
image:
	docker build -t $(IMG) .

.PHONY: image-multiarch
image-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMG) .

.PHONY: docker-push
docker-push:
	docker push $(IMG)
