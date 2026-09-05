# Upstream Master Phase 0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the latest upstream snapshot, its supported product scope, and its OpenAPI difference boundary reproducible and fail-closed before porting any behavior.

**Architecture:** A detached external fixture produces raw collection evidence. Version-controlled Go contracts encode the fixed snapshot, SQLite/Codex/WorkBuddy scope, and API ledger. The parity generator and CI reject evidence drift without mutating the historical v0.1.0 release target.

**Tech Stack:** Go 1.27, `encoding/json/v2`, Go testing, JSON contracts, GitHub Actions, Python 3.11+, uv, pytest.

**Spec:** `docs/superpowers/specs/2026-09-05-upstream-master-phase0-design.md`

## Global Constraints

- Upstream snapshot is exactly `74b961fbb07165595314726715d412a3d0d90589`.
- Go baseline is exactly `e9dce330e58c4acbcfb1c0c5f0a4c9df581750e9`.
- The only supported database is `sqlite`; external agents are only `codex` and `workbuddy`.
- `openapi/powercontext.yaml` remains authoritative and `api/v1` is generated-only.
- Phase 0 must not mark an excluded backend/agent as mapped, passed, or supported.
- Do not change the frozen Oracle, v0.1.0 release target, or runtime product behavior.

---

### Task 1: Collect the upstream snapshot outside source control

**Files:**
- Create outside source control: `D:/test/github/.evidence/powercontext-74b961f-artifacts/meta.json`
- Create outside source control: `D:/test/github/.evidence/powercontext-74b961f-artifacts/pytest-inventory.json`

**Produces:** exact-SHA, lockfile, tool-version, raw-output, normalized-node-ID, and checksum evidence for Task 4.

- [ ] Create detached worktree at `D:/test/github/.evidence/powercontext-74b961f` for `74b961fbb07165595314726715d412a3d0d90589`.
- [ ] Set `UV_PROJECT_ENVIRONMENT` outside both repositories; run `uv lock --locked`, `uv sync --frozen --all-groups`, and `python -c "import scalar_fastapi; import powercontext.server.app"`.
- [ ] Run `python -m pytest --collect-only -q --doctest-modules -p no:cacheprovider` with `PYTHONDONTWRITEBYTECODE=1` and `PYTEST_DISABLE_PLUGIN_AUTOLOAD=1`.
- [ ] Record all inputs, output, exit status, and hashes. A nonzero collection is a recorded blocker, never a success.

### Task 2: Add a fail-closed latest-master product scope contract

**Files:**
- Create: `test/conformance/parity-scope.json`
- Modify: `tools/parity-inventory-generate/main.go`
- Modify: `tools/parity-inventory-generate/main_test.go`

**Produces:** `validateLatestCaseScope(scope parityScope, discoveredCaseIDs []string) error` for Task 4 without blocking frozen v0.1.0 generation.

- [ ] Add failing tests for unknown JSON members, duplicate/unsupported matrix values, missing classification, stale scope entry, duplicate discovery IDs, invalid exclusions, and valid explicit scope entries.
- [ ] Require exactly `sqlite`, `codex`, and `workbuddy`; allow only `unsupported-database`, `unsupported-agent`, and `non-product` exclusions.
- [ ] Represent a node that contains both a supported product claim and a non-target comparison as `mixed`, with an exact supported-surface list, exact non-target dimensions, and a factual English rationale. A mixed node never maps its non-target assertion as supported evidence.
- [ ] Require exactly one rule per discovery case and reject unknown/stale rules.
- [ ] Run `gofmt`, `go test -count=1 ./tools/parity-inventory-generate`, and `git diff --check`.

### Task 3: Add a strict upstream-master OpenAPI ledger

**Files:**
- Create: `test/conformance/upstream-master-openapi-ledger.json`
- Create: `test/conformance/upstream_master_openapi_ledger_test.go`

**Produces:** a direct YAML-derived API classification for later OpenAPI work.

- [ ] Add failing mutants for unknown JSON members, duplicate/missing operations, wrong SHA/counts, missing Handoff Report extension, and an unrecorded `get_stats` migration.
- [ ] Parse both authoritative YAML inputs and require operation counts `common=39`, `upstream_only=38`, `go_only=14`; endpoint counts `upstream_only=39`, `go_only=15`; clusters 10/6/6/6/10; fourteen retained extensions; and `get_stats GET -> POST`.
- [ ] Run `gofmt`, `go test -count=1 ./test/conformance -run UpstreamMasterOpenAPI`, and `git diff --check`.

### Task 4: Commit normalized discovery evidence, classify every case, and wire CI

**Files:**
- Create: `test/conformance/upstream-master-discovery.json`
- Modify: `test/conformance/parity-contract.json`
- Modify: `test/conformance/parity-inventory-rules.json`
- Modify: `test/conformance/parity-inventory.json`
- Modify: `test/conformance/parity_inventory_test.go`
- Modify: `test/conformance/parity_contract_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/migration-gates.yml`

**Consumes:** Task 1 inventory, Task 2 scope validation, and Task 3 API ledger.

- [ ] Write conformance/workflow tests that reject wrong SHA, incomplete scope coverage, missing exclusion rationale, missing regenerated inventory comparison, and a gate that treats OceanBase or retained agents as this plan's success requirement.
- [ ] Commit only normalized path-safe test metadata. Mark unimplemented in-scope cases `pending`; record every exclusion under Task 2's reason contract.
- [ ] Add `make parity-inventory-check`, check out the fixed SHA in CI, regenerate and compare inventory, and preserve frozen Oracle/release gates separately.
- [ ] Run `go test -count=1 ./tools/parity-inventory-generate`, `go test -count=1 ./test/conformance -run 'UpstreamMasterOpenAPI|Parity'`, `make check-generated`, and `git diff --check`.
