GO ?= go
GOFMT ?= gofmt
GOCACHE ?=
GOMODCACHE ?=
PNPM ?= pnpm
UV ?= uv
LICENSE_EYE ?= $(GO) run github.com/apache/skywalking-eyes/cmd/license-eye@v0.8.0

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

.PHONY: generate check-generated module-check contract-test license-check license-fix fmt fmt-check vet \
	test unit-test e2e-test test-sqlite test-race test-full test-oceanbase-live real-provider-test \
	pi-test docs-sync docs-test docs-build harness-sync harness-check harness-compose-check \
	harness-compose-acceptance harness-compose-down build build-full smoke smoke-full check \
	check-portable package-standard package-full clean

generate:
	$(GO) generate ./openapi
	$(GO) run ./tools/mcp-schema-generate

check-generated:
	$(GO) generate ./openapi
	$(GO) run ./tools/mcp-schema-generate
	$(GO) run ./tools/traceability-generate -check
	git diff --exit-code -- openapi api/v1 client/invoker_gen.go internal/mcpapi/schemas_gen.go integrations/dsh/plugins/powercontext/src/operations.generated.ts

module-check:
	$(GO) mod tidy -diff
	$(GO) mod verify

contract-test: check-generated
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' \
		./openapi ./api/v1 ./client ./internal/httpapi ./internal/mcpapi ./server

license-check:
	$(LICENSE_EYE) -c .licenserc.yaml header check

license-fix:
	$(LICENSE_EYE) -c .licenserc.yaml header fix
	$(LICENSE_EYE) -c .licenserc.yaml header check

fmt:
	$(GOFMT) -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

vet:
	$(GO) vet ./...

test: unit-test e2e-test

unit-test:
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' \
		$$(go list ./... | grep -v '/test/e2e$$')

e2e-test:
	CGO_ENABLED=1 $(GO) test -count=1 -tags '$(STANDARD_TAGS)' ./test/e2e
	$(MAKE) smoke VERSION=ci COMMIT=$$(git rev-parse HEAD) BUILD_DATE=1970-01-01T00:00:00Z

test-sqlite:
	CGO_ENABLED=1 $(GO) test -tags '$(STANDARD_TAGS)' ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

test-full:
	@test -d "$(TOKENIZERS_LIB_DIR)" || { echo 'TOKENIZERS_LIB_DIR must contain libtokenizers.a' >&2; exit 2; }
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS) -L$(TOKENIZERS_LIB_DIR)" \
		$(GO) test -tags '$(FULL_TAGS)' ./...

test-oceanbase-live:
	@test -n "$$POWERCONTEXT_TEST_OCEANBASE_URL" || { echo 'POWERCONTEXT_TEST_OCEANBASE_URL must name a dedicated OceanBase MySQL-mode database' >&2; exit 2; }
	$(GO) test -count=1 -run TestLiveOceanBaseProfileSmoke -v ./test/e2e

real-provider-test:
	@test -n "$$POWERCONTEXT_REAL_SMOKE_GENERATION_MODEL$$POWERCONTEXT_REAL_SMOKE_EMBEDDING_MODEL" || \
		{ echo 'set at least one POWERCONTEXT_REAL_SMOKE_*_MODEL variable' >&2; exit 2; }
	$(GO) test -count=1 -run '^TestRealProviderSmoke$$' ./internal/modelprovider

pi-test:
	$(PNPM) --dir integrations/pi/plugins/powercontext install --frozen-lockfile
	$(PNPM) --dir integrations/pi/plugins/powercontext test
	$(PNPM) --dir integrations/pi/plugins/powercontext run typecheck

docs-sync:
	$(UV) sync --project tools/docs --frozen

docs-test:
	$(UV) run --project tools/docs --frozen zensical build -s

docs-build:
	$(UV) run --project tools/docs --frozen zensical build --clean

harness-sync:
	$(GO) mod download
	$(GO) mod verify

harness-check:
	sh -n test/e2e/run.sh
	CGO_ENABLED=1 $(GO) test -run '^$$' -tags '$(STANDARD_TAGS)' ./test/e2e

harness-compose-check:
	POWERCONTEXT_E2E_DATABASE=sqlite test/e2e/run.sh check
	POWERCONTEXT_E2E_DATABASE=oceanbase test/e2e/run.sh check

harness-compose-acceptance:
	test/e2e/run.sh acceptance

harness-compose-down:
	test/e2e/run.sh down

build:
	mkdir -p bin
	CGO_ENABLED=1 $(GO) build -tags '$(STANDARD_TAGS)' -trimpath \
		-ldflags '$(LDFLAGS)' -o bin/powercontext ./cmd/powercontext

build-full:
	@test -d "$(TOKENIZERS_LIB_DIR)" || { echo 'TOKENIZERS_LIB_DIR must contain libtokenizers.a' >&2; exit 2; }
	mkdir -p bin
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS) -L$(TOKENIZERS_LIB_DIR)" \
		$(GO) build -tags '$(FULL_TAGS)' -trimpath -ldflags '$(LDFLAGS)' \
		-o bin/powercontext-full ./cmd/powercontext

smoke: build
	$(GO) run ./tools/process-smoke -binary bin/powercontext -version "$(VERSION)"

smoke-full: build-full
	$(GO) run ./tools/process-smoke -binary bin/powercontext-full -version "$(VERSION)"

package-standard: build
	$(GO) run ./tools/release package \
		-binary bin/powercontext -edition standard \
		-version "$(VERSION)" -commit "$(COMMIT)" -build-date "$(BUILD_DATE)" \
		-output dist -syft "$(SYFT)"

package-full: build-full
	@test -d "$(ONNXRUNTIME_LIB_DIR)" || { echo 'ONNXRUNTIME_LIB_DIR must contain ONNX Runtime libraries' >&2; exit 2; }
	$(GO) run ./tools/release package \
		-binary bin/powercontext-full \
		-onnxruntime-dir "$(ONNXRUNTIME_LIB_DIR)" -edition full \
		-version "$(VERSION)" -commit "$(COMMIT)" -build-date "$(BUILD_DATE)" \
		-output dist -syft "$(SYFT)"

check: module-check fmt-check vet

# Prove the deliberate public SDK surface stays CGO-free and cross-compilable
# for every supported platform. A failure here means a public package gained a
# CGO dependency or platform-specific code that breaks pure-Go consumers.
check-portable:
	@for target in $(PORTABLE_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		printf 'building portable SDK for %s/%s: ' "$$os" "$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(PORTABLE_PACKAGES) && echo OK || exit 1; \
	done

clean:
	$(RM) -r bin dist coverage site
