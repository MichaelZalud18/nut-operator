# Image URL to use for building and pushing the manager image.
IMG ?= ghcr.io/michaelzalud18/nut-operator:main
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
CREATED ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE_SOURCE ?= https://github.com/MichaelZalud18/nut-operator
IMAGE_DOCUMENTATION ?= https://github.com/MichaelZalud18/nut-operator/blob/main/README.md
IMAGE_LICENSES ?= Apache-2.0
IMAGE_REGISTRY ?= ghcr.io/michaelzalud18
IMAGE_TAG ?= main
IMAGE_SHA_TAG ?= sha-$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
NUT_SERVER_IMG ?= $(IMAGE_REGISTRY)/nut-server:$(IMAGE_TAG)
NUT_SERVER_SHA_IMG ?= $(IMAGE_REGISTRY)/nut-server:$(IMAGE_SHA_TAG)
UPSMON_AGENT_IMG ?= $(IMAGE_REGISTRY)/upsmon-agent:$(IMAGE_TAG)
UPSMON_AGENT_SHA_IMG ?= $(IMAGE_REGISTRY)/upsmon-agent:$(IMAGE_SHA_TAG)
NODE_ACTUATOR_IMG ?= $(IMAGE_REGISTRY)/node-actuator:$(IMAGE_TAG)
NODE_ACTUATOR_SHA_IMG ?= $(IMAGE_REGISTRY)/node-actuator:$(IMAGE_SHA_TAG)
# snmpsim-fixture is a test-only fixture image (a simulated SNMP UPS for snmp-ups driver
# conformance testing) -- intentionally not part of docker-build-operands/docker-push/images.yml,
# since it is never a real operand and must never be published as one.
SNMPSIM_FIXTURE_IMG ?= $(IMAGE_REGISTRY)/snmpsim-fixture:$(IMAGE_TAG)
SNMPSIM_FIXTURE_SHA_IMG ?= $(IMAGE_REGISTRY)/snmpsim-fixture:$(IMAGE_SHA_TAG)
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)
# Disable VCS stamping by default so local scaffolds outside a clean git repo still build reproducibly.
GOFLAGS ?= -buildvcs=false
export GOFLAGS

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker
ASH ?= $(shell command -v ash 2>/dev/null || echo $(HOME)/.local/bin/ash)
ASH_MODE ?= local
ASH_OUTPUT_DIR ?= /tmp/nut-operator-ash-output
ASH_OUTPUT_FORMATS ?= aggregated,markdown,sarif,flat-json,html,text

# Scanners excluded by decision, so the summary reads SKIPPED instead of MISSING. The distinction is
# the whole point: MISSING means coverage was wanted and could not run, which is a gap worth
# chasing. SKIPPED means the scanner was assessed and has nothing to contribute here. Leaving these
# MISSING taught everyone to read an incomplete scan as normal.
#
#   cfn-nag  - no CloudFormation in this repository; the deployable surface is Kustomize and images.
#   cdk-nag  - no AWS CDK app to synthesize.
#   opengrep - a semgrep fork, and semgrep already runs in this same scan over the same source.
#
# grype and syft are deliberately NOT here. grype is the only dependency-vulnerability coverage in
# the pipeline (there is no govulncheck), so it is installed rather than excluded.
ASH_EXCLUDED_SCANNERS ?= cfn-nag cdk-nag opengrep
ASH_EXCLUDE_FLAGS = $(foreach scanner,$(ASH_EXCLUDED_SCANNERS),--exclude-scanners $(scanner))
UV_CACHE_DIR ?= /tmp/nut-operator-ash-uv-cache
UV_TOOL_DIR ?= /tmp/nut-operator-ash-uv-tools
export UV_CACHE_DIR
export UV_TOOL_DIR

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= nut-operator-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	# 30m, not go test's 10m default: BeforeSuite builds five operand images, and two of
	# them compile NUT from source (F-39). The default budget was spent on image builds
	# before the suite reached its first assertion.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout=30m
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: security-scan
security-scan: grype syft ## Run AWS Labs ASH security scan locally.
	@PATH="$(LOCALBIN):$$PATH" "$(ASH)" --mode "$(ASH_MODE)" --source-dir "$(CURDIR)" --output-dir "$(ASH_OUTPUT_DIR)" --output-formats "$(ASH_OUTPUT_FORMATS)" $(ASH_EXCLUDE_FLAGS) --no-progress; \
		status=$$?; \
		$(MAKE) --no-print-directory security-triage TRIAGE_FLAGS=--exit-zero; \
		exit $$status

# Reports what ASH's own summary leaves out: which findings are actionable. Runs after the scan and
# preserves the scan's exit status, so the gate is still ASH's verdict and this only makes it legible.
.PHONY: security-triage
security-triage: ## Name the actionable findings from the last ASH scan.
	python3 hack/ash-triage.py --output-dir "$(ASH_OUTPUT_DIR)" $(TRIAGE_FLAGS)

# The CRDs are generated from the Go types; the samples are hand-written. Nothing connected the two
# until this target, so a sample could reference a field shape the API server would reject and only
# a user applying it would find out.
.PHONY: validate-samples
validate-samples: manifests ## Check config/samples and docs/examples against the generated CRD schemas.
	python3 hack/validate-samples.py

# No default for NODE, deliberately. This is the one target where a forgotten variable would pick a
# machine on its own.
.PHONY: verify-actuation
verify-actuation: ## DANGER: POWERS OFF NODE=<node> AND LEAVES IT OFF. Proves the actuate path works on a real kubelet. Needs APPROVE=yes.
	NODE="$(NODE)" AGENT="$(AGENT)" NAMESPACE="$(or $(NAMESPACE),power-system)" APPROVE="$(APPROVE)" ./hack/verify-actuation.sh

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	# --leader-elect=false: leader election defaults to true (F-2), but it needs an in-cluster
	# namespace to create its lease in, which a host process running against kubeconfig doesn't have.
	go run ./cmd/main.go --leader-elect=false

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		-t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-build-nut-server
docker-build-nut-server: ## Build the project-owned NUT server operand image.
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		-f images/nut-server/Dockerfile \
		-t $(NUT_SERVER_IMG) \
		-t $(NUT_SERVER_SHA_IMG) .

.PHONY: docker-build-upsmon-agent
docker-build-upsmon-agent: ## Build the project-owned upsmon node-agent image.
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		-f images/upsmon-agent/Dockerfile \
		-t $(UPSMON_AGENT_IMG) \
		-t $(UPSMON_AGENT_SHA_IMG) .

.PHONY: docker-build-node-actuator
docker-build-node-actuator: ## Build the project-owned node actuator image.
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		-f images/node-actuator/Dockerfile \
		-t $(NODE_ACTUATOR_IMG) \
		-t $(NODE_ACTUATOR_SHA_IMG) .

.PHONY: docker-build-snmpsim-fixture
docker-build-snmpsim-fixture: ## Build the test-only SNMP UPS simulator fixture image (not a real operand, not published).
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		-f images/snmpsim-fixture/Dockerfile \
		-t $(SNMPSIM_FIXTURE_IMG) \
		-t $(SNMPSIM_FIXTURE_SHA_IMG) .

.PHONY: docker-build-operands
docker-build-operands: docker-build-nut-server docker-build-upsmon-agent docker-build-node-actuator ## Build all project-owned operand images.

.PHONY: docker-build-images
docker-build-images: docker-build docker-build-operands ## Build the manager and all project-owned operand images.

.PHONY: docker-smoke-nut-server
docker-smoke-nut-server: ## Smoke test that the NUT server image contains real NUT server tooling.
	hack/smoke-image.sh $(CONTAINER_TOOL) nut-server $(NUT_SERVER_IMG)

.PHONY: docker-smoke-upsmon-agent
docker-smoke-upsmon-agent: ## Smoke test that the upsmon image contains real NUT client tooling.
	hack/smoke-image.sh $(CONTAINER_TOOL) upsmon-agent $(UPSMON_AGENT_IMG)

.PHONY: docker-smoke-node-actuator
docker-smoke-node-actuator: ## Smoke test the node actuator image entrypoint.
	hack/smoke-image.sh $(CONTAINER_TOOL) node-actuator $(NODE_ACTUATOR_IMG)

.PHONY: docker-smoke-nut-tls
docker-smoke-nut-tls: ## Prove the operands negotiate NUT over TLS, not just that the operator renders the directives (F-39, F-40).
	hack/nut-tls-smoke.sh $(CONTAINER_TOOL) $(NUT_SERVER_IMG) $(UPSMON_AGENT_IMG)

.PHONY: docker-smoke-operands
docker-smoke-operands: docker-smoke-nut-server docker-smoke-upsmon-agent docker-smoke-node-actuator docker-smoke-nut-tls ## Smoke test all project-owned operand images.

.PHONY: docker-push-operands
docker-push-operands: ## Push all project-owned operand image tags.
	$(CONTAINER_TOOL) push $(NUT_SERVER_IMG)
	$(CONTAINER_TOOL) push $(NUT_SERVER_SHA_IMG)
	$(CONTAINER_TOOL) push $(UPSMON_AGENT_IMG)
	$(CONTAINER_TOOL) push $(UPSMON_AGENT_SHA_IMG)
	$(CONTAINER_TOOL) push $(NODE_ACTUATOR_IMG)
	$(CONTAINER_TOOL) push $(NODE_ACTUATOR_SHA_IMG)

.PHONY: docker-push-images
docker-push-images: docker-push docker-push-operands ## Push the manager and all project-owned operand image tags.

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	- $(CONTAINER_TOOL) buildx create --name nut-operator-builder
	$(CONTAINER_TOOL) buildx use nut-operator-builder
	- $(CONTAINER_TOOL) buildx build --push \
		--platform=$(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		--build-arg SOURCE=$(IMAGE_SOURCE) \
		--build-arg DOCUMENTATION=$(IMAGE_DOCUMENTATION) \
		--build-arg LICENSES=$(IMAGE_LICENSES) \
		--tag ${IMG} \
		-f Dockerfile .
	- $(CONTAINER_TOOL) buildx rm nut-operator-builder

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

.PHONY: build-installer-byo-cert
build-installer-byo-cert: manifests generate kustomize ## Generate the consolidated YAML with no cert-manager dependency.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/byo-cert > dist/install-byo-cert.yaml

.PHONY: build-catalog
build-catalog: kustomize ## Generate a consolidated YAML with project-maintained capability profiles.
	mkdir -p dist
	"$(KUSTOMIZE)" build config/catalog > dist/catalog.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: deploy-byo-cert
deploy-byo-cert: manifests kustomize ## Deploy the controller with no cert-manager dependency, then provision the webhook certificate.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/byo-cert | "$(KUBECTL)" apply -f -
	KUBECTL="$(KUBECTL)" ./hack/webhook-cert.sh

.PHONY: webhook-cert
webhook-cert: ## Mint or rotate the webhook serving certificate and inject its caBundle. See hack/webhook-cert.sh --help.
	KUBECTL="$(KUBECTL)" ./hack/webhook-cert.sh $(WEBHOOK_CERT_ARGS)

.PHONY: deploy-catalog
deploy-catalog: kustomize ## Deploy project-maintained UPS capability profile catalog CRs.
	"$(KUSTOMIZE)" build config/catalog | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-byo-cert
undeploy-byo-cert: kustomize ## Undeploy the no-cert-manager variant. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/byo-cert | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-catalog
undeploy-catalog: kustomize ## Delete project-maintained UPS capability profile catalog CRs.
	"$(KUSTOMIZE)" build config/catalog | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GRYPE ?= $(LOCALBIN)/grype
SYFT ?= $(LOCALBIN)/syft

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.21.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.12.2

# ASH runs grype and syft when they are on PATH and reports MISSING when they are not. Versions are
# given without a leading v because Anchore's release archives are named that way.
# grype must stay reasonably current: its vulnerability database has a schema version, and an old
# binary fails to hydrate a freshly published database rather than falling back. 0.92.2 could not
# open the current one at all.
GRYPE_VERSION ?= 0.116.1
SYFT_VERSION ?= 1.50.0

# SHA256 digests of the Anchore release archives, from each release's published checksums.txt.
# Bumping a version above means replacing its four lines here; anchore-install-tool fails loudly
# rather than downloading an archive it has no digest for.
define ANCHORE_CHECKSUMS
e5ff3adac317511876de7863598587a7dbab0c47c8e150368b7df06909c11f4e  grype_0.116.1_darwin_amd64.tar.gz
f493f169cbaae48bade169532b20235fc16653d2a044a5bc6fe6f69a3923f975  grype_0.116.1_darwin_arm64.tar.gz
0122df7b655981abe547ad3d2190d65551dac6a2bfc80b4dc2a989b5d0587458  grype_0.116.1_linux_amd64.tar.gz
a8d7504a149629324eb5f4ce3dc25dfd211bbfe047e64ee2bf7844b466c3d84d  grype_0.116.1_linux_arm64.tar.gz
d11a8c7bc27114853bd7c1e1b2f3be3ddda3a1de17aee585329f04c369341c75  syft_1.50.0_darwin_amd64.tar.gz
e32fdb9d47823fa633748a1efca2528fd77c37469ea93c9e40ab835da44e4cce  syft_1.50.0_darwin_arm64.tar.gz
bf7b29ff57f06da30918266a0e1c2885a8f99784798d1bdb1628886aa015d788  syft_1.50.0_linux_amd64.tar.gz
887c57cbcc2d0e8c5c110a4571a3fc7150058b24d74f993ee4663516e5c8ce86  syft_1.50.0_linux_arm64.tar.gz
endef
export ANCHORE_CHECKSUMS
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: grype
grype: $(GRYPE) ## Download grype locally if necessary (ASH dependency-vulnerability scanner).
$(GRYPE): $(LOCALBIN)
	$(call anchore-install-tool,$(GRYPE),grype,$(GRYPE_VERSION))

.PHONY: syft
syft: $(SYFT) ## Download syft locally if necessary (ASH SBOM scanner).
$(SYFT): $(LOCALBIN)
	$(call anchore-install-tool,$(SYFT),syft,$(SYFT_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

# anchore-install-tool downloads a pinned Anchore release archive and verifies its SHA256 before
# unpacking it.
#
# These do not go through go-install-tool because grype's go.mod carries replace directives, so
# `go install` refuses it outright. Fetching the release archive is the supported path -- but the
# upstream instructions pipe a remote script into a shell, which is an unpinned remote execution on
# every run. Pinning the version and verifying a recorded digest is the same discipline the NUT
# operand images already use for their source tarball.
#
# $1 - target path, $2 - tool name, $3 - version without a leading v
define anchore-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
os=$$(go env GOOS) ; arch=$$(go env GOARCH) ;\
archive="$(2)_$(3)_$${os}_$${arch}.tar.gz" ;\
expected=$$(printf '%s\n' "$${ANCHORE_CHECKSUMS}" | awk -v a="$${archive}" '$$2 == a {print $$1}') ;\
[ -n "$${expected}" ] || { echo "No recorded checksum for $${archive}. Add one to ANCHORE_CHECKSUMS." >&2; exit 1; } ;\
tmp="$(LOCALBIN)/.$(2)-$(3).download" ;\
rm -rf "$${tmp}" ; mkdir -p "$${tmp}" ;\
echo "Downloading $${archive}" ;\
curl -sSfL -o "$${tmp}/$${archive}" "https://github.com/anchore/$(2)/releases/download/v$(3)/$${archive}" ;\
actual=$$(sha256sum "$${tmp}/$${archive}" | cut -d' ' -f1) ;\
[ "$${actual}" = "$${expected}" ] || { echo "Checksum mismatch for $${archive}: got $${actual}, want $${expected}" >&2; rm -rf "$${tmp}"; exit 1; } ;\
tar -xzf "$${tmp}/$${archive}" -C "$${tmp}" "$(2)" ;\
rm -f "$(1)" ;\
mv "$${tmp}/$(2)" "$(1)-$(3)" ;\
chmod 0755 "$(1)-$(3)" ;\
rm -rf "$${tmp}" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
