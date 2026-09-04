# Non-Backend Release Archive Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete only Issue #194 Phase 1 by making PowerContext Go release archives carry and prove the exact twelve-integration surface, while excluding every seekDB and OceanBase change.

**Architecture:** Land one strict integration-inventory foundation, then implement staging, command-host consumption, Python-package consumption, archive evidence, and release documentation in isolated file-owned lanes. Integrate on current `main`, preserve exact-Head and post-main evidence, and publish only when the tracker/version ruling makes publication part of Phase 1 closure.

**Tech Stack:** Go 1.27, PowerShell and Bash release workflows, tar/gzip archives, JSON contracts, uv, pnpm, SPDX JSON, GitHub Actions, GitHub Issues and Releases.

**Spec:** `docs/superpowers/specs/2026-09-04-non-backend-release-archive-closure-design.md`

## Global Constraints

- Modify no seekDB or OceanBase source, test, workflow, asset, license, documentation, capability, or Issue checkbox.
- Modify no generated `api/v1`, generated Client, MCP schema, OpenAPI contract, public Go API, persistence format, database behavior, or runtime search capability.
- Preserve `.omx/`, `.workbuddy/`, `.playwright-mcp/`, user worktrees, and unrelated changes.
- Use isolated worktrees based on the current `origin/main`; never implement in `codex/issue-194-l1-l5`.
- Before changing a Go file, run the installed Modern Go Guidelines `list` command for that file or the resolved Go version and read its complete output.
- Use test-first red/green proof for every production behavior change. Configuration-only or documentation-only steps still require the narrow structural validation that proves their contract.
- Keep code comments and maintained documentation in English.
- Never render credentials, prompts, Source or Memory content, raw scope IDs, database locations, configured URLs, private workspace paths, or checkout-local paths in release diagnostics.
- The authoritative release inventory is exactly: `bub`, `claude-code`, `codex`, `dsh`, `hermes`, `langchain`, `langgraph`, `openclaw`, `opencode`, `pi`, `pydantic-ai`, `workbuddy`.
- Command-host consumption covers exactly: `claude-code`, `codex`, `dsh`, `hermes`, `openclaw`, `opencode`, `pi`, `workbuddy`.
- Python-package consumption covers exactly: `bub`, `langchain`, `langgraph`, `pydantic-ai`.
- Standard and Full archives expose the same integration inventory.
- Issue #198 remains non-blocking and cannot be satisfied with a repository-owned or forked synthetic consumer.
- Issue #194 remains open after Phase 1; only its Phase 1 leaf and tracking boxes may be checked.

---

### Task 1: Reconcile Tracker Semantics and Decide the Publication Gate

**Files:**
- External: GitHub Issue #3
- External: GitHub Issue #194
- Read: `docs/release/POLICY.md`
- Read: `.github/release.yml`

**Interfaces:**
- Produces: a read-back verified tracker ruling stating that Issue #194 is the only active execution tracker.
- Produces: a publication ruling of either `published-release-required` or `release-shaped-main-evidence-sufficient`.
- Produces: a release-version decision method based on the exact previous-tag diff; it does not create a tag.

- [ ] **Step 1: Refresh the authoritative state**

Read Issue #3, Issue #194, open PRs, current `origin/main`, the release policy, and the latest published release. Record exact timestamps and SHAs.

- [ ] **Step 2: Update Issue #3 without changing its historical checkbox counts**

Add one comment stating that remaining execution moved to Issue #194, Issue #3 is a closed historical epic, and its old unchecked boxes are not the active inventory.

- [ ] **Step 3: Correct Issue #194 tracker language**

Preserve every checkbox state. Replace the impossible future instruction to close Issue #3 with a final historical reconciliation and closure of Issue #194. State that this delivery handles Phase 1 only and leaves Phases 5-7 untouched.

- [ ] **Step 4: Rule on the publication gate**

Compare current wording and the immutable published `powercontext-v0.1.0` assets. Record one explicit ruling in the execution ledger. If publication is required, Task 9 runs after Task 8; otherwise Task 9 is skipped with the ruling as evidence.

- [ ] **Step 5: Read back both Issues**

Verify the submitted comment/body, unchanged historical checkbox counts, Issue states, and the exact Phase 1/backend boundary.

### Task 2: Add the Strict Release Integration Inventory

**Files:**
- Create: `build/release-integrations.json`
- Create: `tools/release/integration_inventory.go`
- Create: `tools/release/integration_inventory_test.go`

**Interfaces:**
- Produces: `readReleaseIntegrations(repository string) ([]releaseIntegration, error)`.
- Produces: `validateReleaseIntegrations(repository string, integrations []releaseIntegration) error`.
- Produces: `releaseIntegration` with ID, class, required paths, lock paths, and consumer mode.
- Consumers in Tasks 3-6 treat these interfaces and the JSON schema as frozen.

- [ ] **Step 1: Write failing strict-contract tests**

Add table-driven tests that reject duplicate/unknown fields, duplicate IDs, an unsafe absolute or parent path, an invalid class, an empty required path, a missing repository root, a missing required file, a stale inventory entry, and an unclassified `integrations/<root>` directory. Add a valid fixture for the exact twelve integrations.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test -count=1 ./tools/release -run '^TestReleaseIntegrationInventory'`

Expected: FAIL because the inventory parser and validator do not exist.

- [ ] **Step 3: Implement the minimal strict parser and validator**

Use strict JSON decoding with duplicate and unknown field rejection. Normalize repository-relative slash paths, reject absolute and escaping paths before joining, and compare the inventory ID set bidirectionally with first-level `integrations/` directories.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test -count=1 ./tools/release -run '^TestReleaseIntegrationInventory'`

Expected: PASS with all mutants rejected.

- [ ] **Step 5: Run the existing staging and archive-consumer baseline**

Run: `go test -count=1 ./tools/release -run 'Test(StageIntegrationsIncludesEveryRuntimeAdapterAndExcludesWorkspaceState|ReleaseArchiveProvidesConsumableAdapterSources)'`

Expected: PASS; the foundation does not weaken current behavior.

- [ ] **Step 6: Commit the inventory foundation**

```powershell
git add build/release-integrations.json tools/release/integration_inventory.go tools/release/integration_inventory_test.go
git commit -m "test(release): define retained integration inventory"
```

### Task 3: Make Archive Staging Inventory-Driven

**Files:**
- Modify: `tools/release/package.go`
- Modify: `tools/release/files.go`
- Create: `tools/release/integration_staging_test.go`

**Interfaces:**
- Consumes: Task 2 `readReleaseIntegrations` and `releaseIntegration`.
- Produces: staged Standard and Full trees containing the exact inventory entrypoints and lock files.

- [ ] **Step 1: Write failing staging tests**

Prove that both editions contain all reviewed required paths, reject a missing or extra integration root, retain the tracked OpenClaw runtime bundle, and exclude `.venv`, `node_modules`, caches, coverage, `.omx`, `.workbuddy`, `.playwright-mcp`, and unowned generated `dist` trees.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test -count=1 ./tools/release -run '^TestReleaseIntegrationStaging'`

Expected: FAIL because staging does not yet consume and assert the inventory.

- [ ] **Step 3: Implement the minimal inventory-driven staging checks**

Keep the existing safe tree-copy behavior and special tracked OpenClaw bundle handling. Add no backend assets or edition-specific integration set.

- [ ] **Step 4: Run focused and existing archive tests**

Run: `go test -count=1 ./tools/release -run 'Test(ReleaseIntegrationStaging|StageIntegrationsIncludesEveryRuntimeAdapterAndExcludesWorkspaceState)'`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add tools/release/package.go tools/release/files.go tools/release/integration_staging_test.go
git commit -m "test(release): enforce integration staging inventory"
```

### Task 4: Prove Command Hosts Consume the Extracted Archive

**Files:**
- Modify: `tools/release/main_test.go`
- Create if it reduces ownership conflict: `tools/release/command_host_consumer_test.go`

**Interfaces:**
- Consumes: Task 2 inventory and current archive builder.
- Produces: separate extracted-archive setup evidence for the eight command hosts.

- [ ] **Step 1: Write the missing failing host cases**

Split or extend the current consumer test so every command host has an independent subtest. Assert complete fake-host arguments, archive-root paths, OpenCode byte identity and ownership manifest, and WorkBuddy registration of only the extracted `bin/powercontext`.

- [ ] **Step 2: Prove tests reject checkout-local substitution**

Add a mutant or fixture that points one host to the source checkout. Run the focused test and confirm the expected failure before the implementation/test-harness repair.

- [ ] **Step 3: Implement the minimum consumer harness changes**

Do not add new setup commands or change integration behavior. Reuse real CLI entrypoints and boundary fakes only for external host executables.

- [ ] **Step 4: Run the focused archive-consumer test**

Run: `go test -count=1 ./tools/release -run '^TestReleaseArchiveProvidesConsumable(CommandHosts|AdapterSources)$'`

Expected: PASS for all eight host subtests.

- [ ] **Step 5: Commit**

```powershell
git add tools/release/main_test.go tools/release/command_host_consumer_test.go
git commit -m "test(release): prove packaged host setup paths"
```

### Task 5: Prove Python Integrations Install From the Extracted Archive

**Files:**
- Create: `tools/release/archive_python_consumer_test.go`

**Interfaces:**
- Consumes: Task 2 inventory and the current release archive builder.
- Produces: release-only, locked install/import/public-smoke evidence for Bub, LangChain, LangGraph, and Pydantic AI from the actual packaged archive named by `POWERCONTEXT_ARCHIVE`.

- [ ] **Step 1: Write failing extracted-archive package tests**

Put the test behind the explicit `archive_consumer` build tag so ordinary Go and race jobs do not acquire an undeclared `uv` prerequisite. Require `POWERCONTEXT_ARCHIVE` and fail, rather than skip, when the tagged test is selected without it. For each package, copy no checkout files. Extract that actual package artifact, locate the package from the inventory, create an isolated environment through the pinned `uv` path used by CI, install from its declared `uv.lock`, assert the installed editable `direct_url.json` and imported module resolve below the extracted root, and run one public import smoke.

- [ ] **Step 2: Run the focused test and verify RED**

Run the test against a deliberately stripped copy of a real package archive:

`go test -count=1 -tags archive_consumer ./tools/release -run '^TestReleaseArchivePythonAdaptersConsumeExtractedArtifact$'`

Expected: FAIL with the deliberately removed integration path named in the diagnostic. The test must not pass through a skip.

- [ ] **Step 3: Implement the minimum test harness**

Use real `uv` subprocesses and package manifests. Import exactly `PowerContextPlugin`, `PowerContextMiddleware`, `PowerContextRecall` plus `powercontext_tools`, and `PowerContext` plus `PowerContextToolset` from their archived projects. Do not call an external AI provider. If a public smoke needs the Go Server, use the existing SQLite release-shaped binary only.

- [ ] **Step 4: Run the focused test and inspect every package result**

Run: `go test -count=1 -tags archive_consumer ./tools/release -run '^TestReleaseArchivePythonAdaptersConsumeExtractedArtifact$' -v`

Expected: PASS for exactly four packages with no checkout-local import path.

- [ ] **Step 5: Commit**

```powershell
git add tools/release/archive_python_consumer_test.go
git commit -m "test(release): consume packaged Python integrations"
```

### Task 6: Reconcile Archive Evidence and Release Workflows

**Files:**
- Modify: `tools/release/evidence.go`
- Modify: `tools/release/checksum.go`
- Modify: `tools/release/licenses.go`
- Create: `tools/release/integration_evidence_test.go`
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/release-verify.yml`
- Modify: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: Task 2 inventory.
- Produces: release evidence that names every redistributed integration without misclassifying uninstalled lock-file dependencies as bundled runtime dependencies.

- [ ] **Step 1: Write failing evidence and workflow-contract tests**

Reject an omitted integration, an unclassified redistributed bundle, a missing internal checksum, detached SBOM mismatch, Standard/Full inventory drift, checkout-local verification input, skipped-success consumer execution, and missing published-asset verification when publication is required.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test -count=1 ./tools/release -run 'Test(ReleaseIntegrationEvidence|ReleaseWorkflow.*Integration)'`

Expected: FAIL because integration inventory is not yet part of the evidence boundary.

- [ ] **Step 3: Implement minimum evidence/workflow changes**

Preserve current native asset handling. On the Linux amd64 release packaging path, provision Python 3.12 and the repository-pinned `uv 0.10.12`, set `POWERCONTEXT_ARCHIVE` to the actual Standard archive produced by `make package-standard`, and run Task 5's tagged test. Do not put this external resolver prerequisite in ordinary Go/race jobs and do not accept a skip. Add no seekDB or OceanBase dependency, asset, license, job, documentation, or capability claim. Keep third-party Actions pinned and permissions least-privilege.

- [ ] **Step 4: Run focused tests and the release contract**

Run: `go test -count=1 ./tools/release -run 'Test(ReleaseIntegrationEvidence|ReleaseWorkflow.*Integration)'`

Run: `make release-contract-check`

Expected: PASS with bounded, credential-free diagnostics.

- [ ] **Step 5: Commit**

```powershell
git add tools/release/evidence.go tools/release/checksum.go tools/release/licenses.go tools/release/integration_evidence_test.go .github/workflows/release.yml .github/workflows/release-verify.yml tools/release/workflow_test.go
git commit -m "ci(release): verify retained integration evidence"
```

### Task 7: Document the Integration Archive and Decide the Go Release Version

**Files:**
- Modify: `docs/release/INSTALL.md`
- Modify only the retained-integration sections: `README.md`
- Modify only if policy clarification is required: `docs/release/POLICY.md`
- Create: `docs/release/NEXT_VERSION.md`

**Interfaces:**
- Consumes: Task 2 inventory and exact `powercontext-v0.1.0..HEAD` diff.
- Produces: a concrete `v0.1.1` or `v0.2.0` recommendation with compatibility evidence; the coordinator records the final ruling before Task 9.

- [ ] **Step 1: Write the version and documentation contract**

Record public CLI, Go API, OpenAPI, persistence, installation, migration, archive, and adapter changes from the exact previous-tag diff. Explain why the selected version follows repository policy. Do not create a tag or claim an unpublished release exists.

- [ ] **Step 2: Update retained-integration archive documentation**

Document the exact twelve integrations, command-host versus Python-package consumption, WorkBuddy archive-binary ownership, Standard/Full parity, and credential-free archive boundary. Do not alter backend capability text beyond preserving it unchanged.

- [ ] **Step 3: Run documentation validation**

Run: `make check-docs`

Expected: PASS; executable commands remain executable and links resolve under the repository contract.

- [ ] **Step 4: Commit**

```powershell
git add docs/release/INSTALL.md docs/release/POLICY.md docs/release/NEXT_VERSION.md README.md
git commit -m "docs(release): define retained integration archive"
```

### Task 8: Integrate the Parallel Lanes and Verify the Release Candidate

**Files:**
- Merge-only integration across Tasks 3-7
- No new behavior unless a reviewed conflict requires a bounded fix task

**Interfaces:**
- Consumes: reviewed commits from Tasks 3-7.
- Produces: one exact release-candidate Head with all Phase 1 evidence.

- [ ] **Step 1: Rebase every lane on the current intended base**

Refresh `origin/main`, open PRs, Issue #194, and every lane Head. Reconcile changed-file inventories and reject backend paths.

- [ ] **Step 2: Integrate reviewed commits**

Apply the inventory foundation first, then staging, command consumers, Python consumers, evidence/workflows, and documentation. Resolve no semantic conflict without a ledger ruling and focused review.

- [ ] **Step 3: Run focused release tests**

Run the complete `tools/release` package tests and inspect output. Run `make release-contract-check`, `make check-generated`, and `make check-docs` because each directly covers a changed path.

- [ ] **Step 4: Build and inspect Standard and Full release-shaped archives**

Use the repository's pinned native inputs and packaging targets. Confirm exact integration inventory, internal checksums, detached SPDX files, executable modes, and absence of workspace state. Preserve any environment limitation as a gap rather than weakening tests.

- [ ] **Step 5: Request broad independent review**

Review the full diff from the merge base through the exact candidate Head. Fix Critical and Important findings through one bounded implementer/re-review loop.

- [ ] **Step 6: Commit the integrated candidate if integration produced changes**

Use a scoped Conventional Commit message and record the final exact Head in the ledger.

### Task 9: Publish and Verify a Release When Required

**Files:**
- External: Git tag and GitHub Release
- External: GitHub Actions release and release-verify runs

**Interfaces:**
- Consumes: Task 1 publication ruling, Task 7 version ruling, and Task 8 exact candidate Head.
- Produces: published immutable assets and read-back verified release evidence, or a ledgered skip when publication is not required.

- [ ] **Step 1: Evaluate the publication ruling**

If `release-shaped-main-evidence-sufficient`, record Task 9 skipped and continue to Task 10. Otherwise continue.

- [ ] **Step 2: Push the reviewed candidate branch and create the exact release tag**

Confirm the tag does not exist locally or remotely and resolves to the reviewed candidate commit.

- [ ] **Step 3: Generate and inspect the release draft**

Verify exact previous-tag comparison, version, notes, archive inventory, checksums, SPDX assets, image digests, and absence of backend completion claims.

- [ ] **Step 4: Publish and run published-release verification**

Use the repository's existing explicit publish workflow. Download the published artifacts and verify them independently. Record every workflow URL and conclusion.

- [ ] **Step 5: Read back the tag, release, assets, digests, and post-release workflows**

Do not treat dispatch success or draft creation as publication proof.

### Task 10: Update Only Phase 1 and Close the Execution Ledger

**Files:**
- External: GitHub Issue #194
- External: GitHub Issue #3 readback

**Interfaces:**
- Consumes: Task 8 exact-Head/post-main evidence and, when required, Task 9 published-release evidence.
- Produces: checked Phase 1 leaf and tracking boxes with all backend boxes unchanged.

- [ ] **Step 1: Perform the completion audit**

Verify implementation, commits, pushed branches, PRs, exact-Head checks, merges, current main, post-main checks, archive evidence, documentation, and any required published release separately.

- [ ] **Step 2: Update Phase 1 evidence and checkboxes**

Preserve every existing checked item and every backend checkbox. Record exact SHAs and workflow URLs.

- [ ] **Step 3: Read back Issue #194**

Confirm Phase 1's four leaf boxes and parent tracking box are checked, Phases 5-7 are unchanged, the Issue remains open, and the expected unchecked count is 25.

- [ ] **Step 4: Run the final repository and remote-state audit**

Refresh `origin/main`, open PRs, current release state, and exact-main workflows. Record remaining gaps without claiming Issue #194 is complete.
