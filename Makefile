SHELL := bash
export SHELLOPTS := errexit:nounset:pipefail
.DEFAULT_GOAL := help
.DELETE_ON_ERROR:
.SUFFIXES:
MAKEFLAGS += --no-builtin-rules

GO ?= go
GOFMT ?= gofmt
GOCACHE ?=
GOMODCACHE ?=
PNPM ?= pnpm
UV ?= uv
LICENSE_EYE ?= $(GO) run github.com/apache/skywalking-eyes/cmd/license-eye@v0.8.0

TOOLS_BIN := $(CURDIR)/.tools/bin
GOLANGCI_LINT_VERSION := v2.13.1
GOLANGCI_LINT_VERSION_TEXT := $(patsubst v%,%,$(GOLANGCI_LINT_VERSION))
GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint$(shell $(GO) env GOEXE)
PROJECT_GO_TOOLCHAIN = $(shell $(GO) env GOVERSION)
GOLANGCI_LINT_STAMP = $(TOOLS_BIN)/.golangci-lint-$(GOLANGCI_LINT_VERSION)-$(PROJECT_GO_TOOLCHAIN)
APIDIFF_VERSION := v0.0.0-20260824195058-e88cd73687aa
APIDIFF := $(TOOLS_BIN)/apidiff$(shell $(GO) env GOEXE)
APIDIFF_STAMP = $(TOOLS_BIN)/.apidiff-$(APIDIFF_VERSION)-$(PROJECT_GO_TOOLCHAIN)

COVERAGE_DIR ?= coverage
COVERAGE_PROFILE ?= $(COVERAGE_DIR)/coverage.out
COVERAGE_SUMMARY ?= $(COVERAGE_DIR)/summary.txt
COVERAGE_MINIMUM ?= 16.0
LICENSE_DEPENDENCY_OUTPUT ?= $(COVERAGE_DIR)/dependencies.json

STANDARD_TAGS := sqlite_fts5
FULL_TAGS := sqlite_fts5,local_embeddings,ORT
VERSION ?= devel
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

# Deliberate public packages that downstream Go consumers must be able to
# import without CGO, matching the platform matrix used by CI.
PORTABLE_PACKAGES := ./api/... ./artifact/... ./client/... ./inference/... ./openapi/... ./source/... ./trigger/...
PORTABLE_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
DOWNSTREAM_DIR := test/downstream
DOWNSTREAM_BINARY := $(CURDIR)/bin/powercontext-downstream$(shell $(GO) env GOEXE)
MODULE_INVENTORY := test/module-inventory.json
OWNED_MODULES := . $(DOWNSTREAM_DIR)
MODULE_PATH := github.com/ob-labs/powercontext-go
API_BASELINE := test/api-compat/pre-release.apidiff
API_BASELINE_GENERATOR ?= $(GO) run ./tools/api-baseline
API_COMPAT_GOFLAGS = $(strip $(GOFLAGS) -trimpath)
PUBLIC_API_PACKAGES := \
	$(MODULE_PATH)/api/v1 \
	$(MODULE_PATH)/artifact \
	$(MODULE_PATH)/artifact/experience \
	$(MODULE_PATH)/artifact/handoff \
	$(MODULE_PATH)/artifact/memory \
	$(MODULE_PATH)/artifact/skill \
	$(MODULE_PATH)/client \
	$(MODULE_PATH)/inference \
	$(MODULE_PATH)/server \
	$(MODULE_PATH)/source \
	$(MODULE_PATH)/trigger

.PHONY: help lint-tools lint lint-fix api-compat-tools api-baseline api-compat generate check-generated module-check module-inventory module-integrity contract-test license-check license-fix license-dependencies fmt fmt-check vet build-all coverage coverage-check governance-check \
	test unit-test e2e-test test-sqlite test-race test-full test-oceanbase-live real-provider-test \
	pi-test docs-sync docs-test docs-build harness-sync harness-check harness-compose-check \
	harness-compose-acceptance harness-compose-down build build-full smoke smoke-full check \
	check-portable generated-consumers downstream-compat package-standard package-full clean

help: ## Show supported development, verification, and release commands.
	@awk 'BEGIN { FS = ":.*##"; print "Supported targets:" } /^[[:alnum:]_-]+:.*##/ { printf "  %-28s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

$(GOLANGCI_LINT): Makefile go.mod
	@mkdir -p "$(TOOLS_BIN)"
	GOTOOLCHAIN="$(PROJECT_GO_TOOLCHAIN)+auto" GOBIN="$(TOOLS_BIN)" \
		$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOLANGCI_LINT_STAMP): $(GOLANGCI_LINT)
	@"$(GOLANGCI_LINT)" --version | grep -q "version $(GOLANGCI_LINT_VERSION_TEXT) "
	@$(RM) $(TOOLS_BIN)/.golangci-lint-*
	@touch "$@"

lint-tools: $(GOLANGCI_LINT_STAMP) ## Install the pinned lint tool under .tools/bin.

lint: lint-tools ## Run the pinned lint policy.
	"$(GOLANGCI_LINT)" run

lint-fix: lint-tools ## Format and apply supported automatic lint fixes.
	"$(GOLANGCI_LINT)" fmt
	"$(GOLANGCI_LINT)" run --fix

$(APIDIFF): Makefile go.mod
	@mkdir -p "$(TOOLS_BIN)"
	GOTOOLCHAIN="$(PROJECT_GO_TOOLCHAIN)+auto" GOBIN="$(TOOLS_BIN)" \
		$(GO) install golang.org/x/exp/cmd/apidiff@$(APIDIFF_VERSION)

$(APIDIFF_STAMP): $(APIDIFF)
	@$(GO) version -m "$(APIDIFF)" | awk '$$1 == "mod" && $$2 == "golang.org/x/exp" && $$3 == "$(APIDIFF_VERSION)" { found = 1 } END { exit !found }'
	@touch "$@"

api-compat-tools: $(APIDIFF_STAMP) ## Install the pinned public API compatibility tool.

api-baseline: ## Regenerate the approved pre-release public API baseline.
	GOFLAGS="$(API_COMPAT_GOFLAGS)" $(API_BASELINE_GENERATOR) \
		-output "$(API_BASELINE)" -module "$(MODULE_PATH)" $(PUBLIC_API_PACKAGES)

api-compat: api-compat-tools ## Reject incompatible changes to deliberate public Go packages.
	@if [ ! -f "$(API_BASELINE)" ]; then \
		printf 'missing public API baseline: %s\n' "$(API_BASELINE)" >&2; \
		exit 1; \
	fi
	@GOFLAGS="$(API_COMPAT_GOFLAGS)" $(API_BASELINE_GENERATOR) \
		-check "$(API_BASELINE)" -module "$(MODULE_PATH)" $(PUBLIC_API_PACKAGES)
	@set -eu; \
	current=$$(mktemp); \
	trap 'rm -f "$$current"' EXIT; \
	GOFLAGS="$(API_COMPAT_GOFLAGS)" $(API_BASELINE_GENERATOR) \
		-output "$$current" -module "$(MODULE_PATH)" $(PUBLIC_API_PACKAGES); \
	report=$$(GOFLAGS="$(API_COMPAT_GOFLAGS)" "$(APIDIFF)" -m -incompatible "$(API_BASELINE)" "$$current"); \
	if [ -n "$$report" ]; then \
		printf 'incompatible public API change:\n%s\n' "$$report" >&2; \
		exit 1; \
	fi
	@POWERCONTEXT_APIDIFF="$(APIDIFF)" GOFLAGS="$(API_COMPAT_GOFLAGS)" \
		$(GO) test -count=1 ./tools/api-baseline -run '^TestAPIDiffRejectsRemovedExportedIdentifier$$'

generate: ## Regenerate checked-in OpenAPI and MCP outputs.
	$(GO) generate ./openapi
	$(GO) run ./tools/mcp-schema-generate

check-generated: ## Verify generated contracts and traceability outputs are clean.
	$(GO) generate ./openapi
	$(GO) run ./tools/mcp-schema-generate
	$(GO) run ./tools/traceability-generate -check
	git diff --exit-code -- openapi api/v1 client/invoker_gen.go internal/mcpapi/schemas_gen.go integrations/dsh/plugins/powercontext/src/operations.generated.ts

generated-consumers: lint-tools ## Generate, test, and lint fresh consumers from public generator CLIs.
	POWERCONTEXT_GOLANGCI_LINT="$(GOLANGCI_LINT)" GOWORK=off $(GO) test -count=1 ./tools/generated-consumers

module-check: ## Verify tidy readonly module metadata and checksums.
	$(GO) mod tidy -diff
	$(GO) mod verify

module-inventory: ## Verify every owned Go module has an explicit inventory entry.
	$(GO) run ./tools/module-integrity -inventory "$(MODULE_INVENTORY)"

module-integrity: module-inventory lint-tools ## Verify metadata, readonly build, tests, and lint for every owned Go module.
	@set -eu; \
	for module in $(OWNED_MODULES); do \
		printf 'checking Go module %s\n' "$$module"; \
		GOWORK=off $(GO) -C "$$module" mod tidy -diff; \
		GOWORK=off $(GO) -C "$$module" mod verify; \
		GOWORK=off $(GO) -C "$$module" build -mod=readonly ./...; \
		if [ "$$module" = "$(DOWNSTREAM_DIR)" ]; then \
			$(GO) build -tags '$(STANDARD_TAGS)' -o "$(DOWNSTREAM_BINARY)" ./cmd/powercontext; \
			POWERCONTEXT_DOWNSTREAM_BINARY="$(DOWNSTREAM_BINARY)" GOWORK=off $(GO) -C "$$module" test -count=1 -mod=readonly ./...; \
		else \
			GOWORK=off $(GO) -C "$$module" test -count=1 -mod=readonly ./...; \
		fi; \
		(cd "$$module" && GOWORK=off "$(GOLANGCI_LINT)" run --config "$(CURDIR)/.golangci.yml"); \
	done

contract-test: check-generated ## Test the generated OpenAPI transport contract.
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' \
		./openapi ./api/v1 ./client ./internal/httpapi ./internal/mcpapi ./server

license-check: ## Verify required Apache-2.0 source headers.
	$(LICENSE_EYE) -c .licenserc.yaml header check

license-fix: ## Repair eligible source headers and recheck them.
	$(LICENSE_EYE) -c .licenserc.yaml header fix
	$(LICENSE_EYE) -c .licenserc.yaml header check

license-dependencies: build ## Build bounded dependency-license evidence for the standard binary.
	@$(RM) "$(LICENSE_DEPENDENCY_OUTPUT)"
	$(GO) run ./tools/release licenses -binary bin/powercontext -edition standard -output "$(LICENSE_DEPENDENCY_OUTPUT)"

fmt: lint-tools ## Format supported source with the pinned formatter set.
	$(GOFMT) -w $$(find . -name '*.go' -not -path './vendor/*')
	"$(GOLANGCI_LINT)" fmt

fmt-check: lint-tools ## Verify supported source formatting without edits.
	@$(GOFMT) -l $$(find . -name '*.go' -not -path './vendor/*') >/dev/null
	"$(GOLANGCI_LINT)" fmt --diff

vet: ## Run Go vet across all packages.
	$(GO) vet ./...

build-all: ## Build all Go packages with readonly module resolution.
	$(GO) build -mod=readonly ./...

test: unit-test e2e-test ## Run the standard unit and end-to-end suites.

unit-test: ## Run standard-tag unit tests.
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' \
		$$(go list ./... | grep -v '/test/e2e$$')

e2e-test: ## Run standard-tag process-level end-to-end tests.
	CGO_ENABLED=1 $(GO) test -count=1 -tags '$(STANDARD_TAGS)' ./test/e2e
	$(MAKE) smoke VERSION=ci COMMIT=$$(git rev-parse HEAD) BUILD_DATE=1970-01-01T00:00:00Z

test-sqlite: ## Run all standard-tag tests against SQLite.
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' ./...

test-race: ## Run all Go tests with the race detector.
	CGO_ENABLED=1 $(GO) test -race ./...

coverage: ## Generate race-enabled atomic coverage and enforce its baseline.
	@mkdir -p "$(COVERAGE_DIR)"
	CGO_ENABLED=1 $(GO) test -race -covermode=atomic -coverprofile="$(COVERAGE_PROFILE)" ./...
	$(GO) tool cover -func="$(COVERAGE_PROFILE)" > "$(COVERAGE_SUMMARY)"
	@tail -n 1 "$(COVERAGE_SUMMARY)"
	@$(MAKE) coverage-check

coverage-check: ## Check an existing coverage summary against the minimum.
	@test -s "$(COVERAGE_SUMMARY)" || { echo 'coverage summary is missing or empty' >&2; exit 2; }
	@actual=$$(awk 'END { value = $$3; sub(/%$$/, "", value); print value }' "$(COVERAGE_SUMMARY)"); \
		minimum="$(COVERAGE_MINIMUM)"; \
		if ! awk -v value="$$actual" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$$/) }'; then \
			printf 'coverage total must be a non-negative number, got %s\n' "$$actual" >&2; \
			exit 2; \
		fi; \
		if ! awk -v value="$$minimum" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$$/) }'; then \
			printf 'coverage minimum must be a non-negative number, got %s\n' "$$minimum" >&2; \
			exit 2; \
		fi; \
		if awk -v actual="$$actual" -v minimum="$$minimum" 'BEGIN { exit !((actual + 0) >= (minimum + 0)) }'; then \
			printf 'coverage %s%% meets minimum %s%%\n' "$$actual" "$$minimum"; \
		else \
			printf 'coverage %s%% is below minimum %s%%\n' "$$actual" "$$minimum" >&2; \
			exit 1; \
		fi

governance-check: ## Verify contribution, review, and workflow contracts.
	$(GO) run ./tools/governance-check

test-full: ## Run Full native-asset tests.
	@test -d "$(TOKENIZERS_LIB_DIR)" || { echo 'TOKENIZERS_LIB_DIR must contain libtokenizers.a' >&2; exit 2; }
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS) -L$(TOKENIZERS_LIB_DIR)" \
		$(GO) test -tags '$(FULL_TAGS)' ./...

test-oceanbase-live: ## Run the disposable live OceanBase compatibility smoke test.
	@test -n "$${POWERCONTEXT_TEST_OCEANBASE_URL:-}" || { echo 'POWERCONTEXT_TEST_OCEANBASE_URL must name a dedicated OceanBase MySQL-mode database' >&2; exit 2; }
	$(GO) test -count=1 -run TestLiveOceanBaseProfileSmoke -v ./test/e2e

real-provider-test: ## Run explicit credentialed real-provider smoke tests.
	@test -n "$${POWERCONTEXT_REAL_SMOKE_GENERATION_MODEL:-}$${POWERCONTEXT_REAL_SMOKE_EMBEDDING_MODEL:-}" || \
		{ echo 'set at least one POWERCONTEXT_REAL_SMOKE_*_MODEL variable' >&2; exit 2; }
	$(GO) test -count=1 -run '^TestRealProviderSmoke$$' ./internal/modelprovider

pi-test: ## Install, test, and type-check the Pi adapter package.
	$(PNPM) --dir integrations/pi/plugins/powercontext install --frozen-lockfile
	$(PNPM) --dir integrations/pi/plugins/powercontext test
	$(PNPM) --dir integrations/pi/plugins/powercontext run typecheck

docs-sync: ## Synchronize the locked documentation environment.
	$(UV) sync --project tools/docs --frozen

docs-test: ## Strictly build documentation without publishing it.
	$(UV) run --project tools/docs --frozen zensical build -s

docs-build: ## Clean and build the documentation site.
	$(UV) run --project tools/docs --frozen zensical build --clean

harness-sync: ## Download and verify the E2E harness module graph.
	$(GO) mod download
	$(GO) mod verify

harness-check: ## Validate E2E harness scripts and compile the test package.
	sh -n test/e2e/run.sh
	CGO_ENABLED=1 $(GO) test -run '^$$' -tags '$(STANDARD_TAGS)' ./test/e2e

harness-compose-check: ## Validate SQLite and OceanBase harness definitions.
	POWERCONTEXT_E2E_DATABASE=sqlite test/e2e/run.sh check
	POWERCONTEXT_E2E_DATABASE=oceanbase test/e2e/run.sh check

harness-compose-acceptance: ## Run the containerized E2E acceptance harness.
	test/e2e/run.sh acceptance

harness-compose-down: ## Stop the E2E harness containers.
	test/e2e/run.sh down

build: ## Build the standard PowerContext server binary.
	mkdir -p bin
	CGO_ENABLED=1 $(GO) build -tags '$(STANDARD_TAGS)' -trimpath \
		-ldflags '$(LDFLAGS)' -o bin/powercontext ./cmd/powercontext

build-full: ## Build the Full native-asset server binary.
	@test -d "$(TOKENIZERS_LIB_DIR)" || { echo 'TOKENIZERS_LIB_DIR must contain libtokenizers.a' >&2; exit 2; }
	mkdir -p bin
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS) -L$(TOKENIZERS_LIB_DIR)" \
		$(GO) build -tags '$(FULL_TAGS)' -trimpath -ldflags '$(LDFLAGS)' \
		-o bin/powercontext-full ./cmd/powercontext

smoke: build ## Build and smoke-test the standard server binary.
	$(GO) run ./tools/process-smoke -binary bin/powercontext -version "$(VERSION)"

smoke-full: build-full ## Build and smoke-test the Full server binary.
	$(GO) run ./tools/process-smoke -binary bin/powercontext-full -version "$(VERSION)"

package-standard: build ## Build a standard release archive and SBOM.
	$(GO) run ./tools/release package \
		-binary bin/powercontext -edition standard \
		-version "$(VERSION)" -commit "$(COMMIT)" -build-date "$(BUILD_DATE)" \
		-output dist -syft "$(SYFT)"

package-full: build-full ## Build a Full release archive and SBOM.
	@test -d "$(ONNXRUNTIME_LIB_DIR)" || { echo 'ONNXRUNTIME_LIB_DIR must contain ONNX Runtime libraries' >&2; exit 2; }
	$(GO) run ./tools/release package \
		-binary bin/powercontext-full \
		-onnxruntime-dir "$(ONNXRUNTIME_LIB_DIR)" -edition full \
		-version "$(VERSION)" -commit "$(COMMIT)" -build-date "$(BUILD_DATE)" \
		-output dist -syft "$(SYFT)"

check: module-check fmt-check vet ## Run the core module, formatting, and vet checks.

# Prove the deliberate public SDK surface stays CGO-free and cross-compilable
# for every supported platform. A failure here means a public package gained a
# CGO dependency or platform-specific code that breaks pure-Go consumers.
check-portable: ## Cross-build the deliberate public SDK packages without CGO.
	@for target in $(PORTABLE_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		printf 'building portable SDK for %s/%s: ' "$$os" "$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(PORTABLE_PACKAGES) && echo OK || exit 1; \
	done

downstream-compat: ## Test the current checkout from an isolated public Go module.
	$(GO) build -tags '$(STANDARD_TAGS)' -o "$(DOWNSTREAM_BINARY)" ./cmd/powercontext
	GOWORK=off $(GO) -C "$(DOWNSTREAM_DIR)" mod tidy -diff
	GOWORK=off $(GO) -C "$(DOWNSTREAM_DIR)" mod verify
	GOWORK=off $(GO) -C "$(DOWNSTREAM_DIR)" build -mod=readonly ./...
	POWERCONTEXT_DOWNSTREAM_BINARY="$(DOWNSTREAM_BINARY)" GOWORK=off $(GO) -C "$(DOWNSTREAM_DIR)" test -count=1 -mod=readonly ./...

clean: ## Remove known local build, distribution, coverage, and site outputs.
	$(RM) -r bin dist coverage site
