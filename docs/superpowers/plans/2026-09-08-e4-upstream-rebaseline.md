# E4 Upstream Rebaseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the current-master compatibility inventory and conformance evidence accurately track upstream e4 without changing frozen or already-shipped canonical sidecar wire contracts.

**Architecture:** The compatibility ledger owns the moving e4 operation inventory. Each canonical sidecar manifest owns its stable raw OpenAPI source and digest. Generator validation joins them only at the selected operation endpoints and implementation status; it no longer demands full-inventory equality between two intentionally different pins.

**Tech Stack:** Go 1.27, JSON v2, OpenAPI YAML, existing `tools/api-generate`, conformance ledger, exact Git checkout, pytest/uv discovery evidence.

**Spec:** `docs/superpowers/specs/2026-09-08-e4-upstream-rebaseline-design.md`

## Global Constraints

- Preserve the frozen legacy OpenAPI, `api/v1`, Client Invoker, and MCP generated inventory byte-for-byte.
- Keep Scope, Source, and Artifact sidecar raw source manifests on 74 until their new e4 schema semantics have a dedicated runtime implementation.
- Current-master e4 inventory must contain all 94 operations, with all 17 new operations explicitly `deferred`.
- Do not add or advertise seekDB, OceanBase, or external agents other than Codex and WorkBuddy.
- Current-master discovery evidence must come from an exact e4 checkout and real collection output; do not synthesize node IDs, counts, or hashes.

---

### Task 1: Separate Inventory and Sidecar Validation

**Files:**
- Modify: `tools/api-generate/compatibility.go`, `tools/api-generate/canonical_scopes.go`
- Modify: `tools/api-generate/compatibility_test.go`, `tools/api-generate/canonical_scopes_test.go`, Source/Artifact sidecar tests when their shared validation expectation changes
- Modify: `openapi/compatibility-surface.json`
- Modify: `test/conformance/upstream-master-openapi-ledger.json`, `test/conformance/upstream_master_openapi_ledger_test.go`

**Interfaces:**
- Consumes: e4 compatibility entries and 74-pinned sidecar manifests.
- Produces: validation that requires every selected sidecar operation to be e4-inventory-identical and `implemented-canonical`, without requiring the 74 raw sidecar source to contain every e4 deferred operation.

- [ ] **Step 1: Add failing validator tests**

Create a compatibility surface test fixture with e4 counts and all e4 deferred
entries while a sidecar source remains the 74 raw blob. Assert the old
full-operation comparison rejects that intentionally stable source. Add
mutants that change a selected operation path, remove it from the inventory,
or change its status to `deferred`; every mutant must reject.

- [ ] **Step 2: Run the focused red test**

Run:

```powershell
go test -count=1 ./tools/api-generate -run 'Compatibility|ScopeSidecar|SourceSidecar|ArtifactSidecar'
```

Expected: the e4-inventory/74-sidecar fixture fails only at the obsolete full
inventory equality assertion.

- [ ] **Step 3: Implement the narrow validator split**

Keep `compatibilitySurface.validate` responsible for the frozen legacy
baseline, e4 operation count, staged operation uniqueness/status, retained
extensions, method migration, and MCP allowlist. Replace the sidecar
full-inventory comparison with selected-operation validation against the
decoded compatibility surface. Preserve raw source operation parsing only to
verify selected operation presence/method/path, while the manifest SHA-256
continues to validate its exact bytes.

- [ ] **Step 4: Update e4 compatibility surface**

Set the upstream commit to `e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c`,
canonical count to 94, and insert the 17 e4 operation records in lexical
operation-ID order with `deferred` status. Preserve every existing status and
all frozen/retained/migration/MCP fields.

Update the paired OpenAPI ledger in the same task because the generator's
full compatibility test intentionally asserts byte-for-byte inventory
agreement. Set its e4 identity, operation and endpoint counts, new cluster
counts, and all 17 exact records. Do not update master pytest node/discovery
evidence yet.

- [ ] **Step 5: Verify the generator boundary**

Run the focused tests above and:

```powershell
go test -count=1 ./tools/api-generate
make check-generated
git diff --check
```

Expected: all selected 74 sidecars regenerate identically; no `api/v1`,
canonical generated artifact, Client Invoker, or MCP schema file changes.

- [ ] **Step 6: Commit Task 1**

```powershell
git add tools/api-generate openapi/compatibility-surface.json test/conformance/upstream-master-openapi-ledger.json test/conformance/upstream_master_openapi_ledger_test.go
git commit -m 'feat(openapi): separate latest inventory from sidecar pins'
```

### Task 2: Rebuild Exact E4 Conformance Evidence

**Files:**
- Modify: `tools/parity-inventory-generate/main.go`, its tests, and exact pin constants
- Modify: `Makefile`, `.github/workflows/migration-gates.yml`
- Modify: `test/conformance/upstream-master-node-ids.json`, `upstream-master-discovery.json`, `parity-scope.json`, and discovery test constants only from real collection output

**Interfaces:**
- Consumes: an exact e4 upstream checkout, raw e4 OpenAPI, pinned Python/uv/pytest discovery output.
- Produces: a reproducible e4 master ledger and all-node classification gate.

- [ ] **Step 1: Create a real e4 evidence checkout and red proof**

Fetch/check out exactly `e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c`
outside the Go worktree. Run the existing OpenAPI ledger comparison against it
before editing evidence. Expected: current 74 pin/count/entry assertions fail.

- [ ] **Step 2: Update e4 ledger and validators**

Use the Task 1 e4 OpenAPI ledger as immutable input. Do not alter operation
counts, endpoint identities, or cluster entries in this task; discovery must
only classify exact node IDs against that ledger.

Retain `e9dce330e58c4acbcfb1c0c5f0a4c9df581750e9` as the Go OpenAPI operation
baseline. Its `e9dce330..9b45f7f` delta changes only `Capabilities` fields;
operation IDs, methods, and paths are identical, so it does not require an
inventory change. If a future Go baseline delta changes any operation identity,
update that baseline separately with endpoint-comparison evidence before
changing the ledger.

- [ ] **Step 3: Regenerate discovery evidence**

Use the exact e4 checkout and its pinned environment to run the repository
collector. Update node IDs, raw stdout digest, node manifest digest, Python/
pytest/uv-lock identities, node count, and full scope classification solely
from its output. Do not modify release `parity-contract.json`,
`parity-inventory.json`, or release target deltas.

- [ ] **Step 4: Update exact gate pins**

Update Make and migration workflow exact e4 checkout checks, conformance
constants, and parity-inventory generator constants. Add/adjust mutants so a
74 checkout, a missing e4 operation, an unknown cluster, or an unclassified
node fails before claiming a current-master inventory.

- [ ] **Step 5: Verify real e4 evidence**

Run:

```powershell
$env:CGO_CPPFLAGS='-I' + (Join-Path $env:TEMP 'powercontext-sqlite-header-v1.14.33')
$env:POWERCONTEXT_UPSTREAM_CHECKOUT='<exact-e4-checkout>'
go test -count=1 ./test/conformance -run '^TestUpstreamMaster(OpenAPI|Discovery)'
make parity-inventory-check UPSTREAM_MASTER_CHECKOUT="$env:POWERCONTEXT_UPSTREAM_CHECKOUT"
go test -count=1 ./tools/parity-inventory-generate
git diff --check
```

- [ ] **Step 6: Commit Task 2**

```powershell
git add test/conformance tools/parity-inventory-generate Makefile .github/workflows/migration-gates.yml
git commit -m 'feat(conformance): rebaseline current upstream inventory to e4'
```

### Task 3: Document the Two-Pin Policy

**Files:**
- Modify: `openapi/README.md`, maintained migration/conformance documentation that names the old snapshot
- Test: existing documentation/workflow contracts only when a changed statement is executable

**Interfaces:**
- Consumes: Task 1 sidecar/inventory validation and Task 2 e4 evidence.
- Produces: documentation that distinguishes latest-master inventory pin from stable sidecar schema pins and lists deferred e4 clusters.

- [ ] **Step 1: Write a documentation contract check or update an existing targeted assertion**

Assert documentation names e4 as the current-master inventory while Scope,
Source, and Artifact manifests remain independently pinned to 74. Assert no
statement claims e4 access/prompt/tag schema behavior is implemented.

- [ ] **Step 2: Run the red documentation assertion**

Run the changed documentation contract test. Expected: old 38/77 single-pin
wording fails.

- [ ] **Step 3: Update maintained OpenAPI documentation**

Describe 94 canonical operations, 55 upstream-only operations, 17 newly
deferred entries, the two-pin validation rule, and the frozen legacy/MCP
boundary. Do not advertise tags, prompts, access, Artifact writes, managed
Skill lifecycle, remote targets, or native personal services as implemented.

- [ ] **Step 4: Verify final rebaseline gates**

Run:

```powershell
go test -count=1 ./tools/api-generate ./tools/parity-inventory-generate
make check-generated
make contract-test
make SHELL=D:/programs/cmder/vendor/git-for-windows/bin/bash.exe api-compat
make license-check
git diff --check
```

- [ ] **Step 5: Commit Task 3**

```powershell
git add openapi/README.md docs .github test/conformance tools
git commit -m 'docs(openapi): record e4 inventory and sidecar pins'
```
