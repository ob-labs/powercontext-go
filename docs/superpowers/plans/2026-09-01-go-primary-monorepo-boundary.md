# Go-primary Monorepo Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep all assets in `powercontext-go` while making Go the documented and GitHub-classified primary product, and limiting pre-WP6 acceptance to Codex, WorkBuddy, and SQLite.

**Architecture:** The repository remains a monorepo. `.gitattributes` excludes maintained auxiliary sources from GitHub language statistics without calling them vendored or generated; a Go contract test prevents classification drift. The existing `host-adapters` job keeps its stable required identity but runs Codex and WorkBuddy only, while a separate retained-agent job preserves executable P3 evidence. Documentation and Issue #3 state the same boundary.

**Tech Stack:** Go 1.27.0, Go test, GitHub Actions YAML, actionlint, GitHub CLI, Zensical documentation build, Git attributes.

**Spec:** `docs/superpowers/specs/2026-09-01-go-primary-monorepo-boundary-design.md`

## Global Constraints

- Work in the isolated `codex/monorepo-boundary` worktree created from current `origin/main`; do not edit the stale root checkout or its `.omx/` and `.workbuddy/` directories.
- Preserve every tracked adapter, evaluation asset, fixture, lock file, and completed Issue #3 checkbox.
- Keep `host-adapters` as the stable pre-WP6 required job identifier; never use a path filter that can make it disappear.
- Use `-linguist-detectable` only. Do not add `linguist-vendored` or `linguist-generated` to any maintained project path.
- Do not modify Go runtime behavior, public APIs, database formats, OpenAPI, generated contracts, or seekDB/OceanBase scope.
- Follow Go 1.27 guidelines for any changed Go test file; run the Modern Go Guidelines `list` command for that file before editing it.
- Every external GitHub state claim must be reread after mutation; GitHub language statistics may be asynchronous and must be verified after the merge reaches default branch.

---

### Task 1: Pin the GitHub language-classification boundary

**Files:**
- Modify: `.gitattributes`
- Modify: `tools/release/workflow_test.go`
- Test: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: tracked monorepo roots `evaluation/`, `integrations/`, `test/conformance/*.py`, and `test/differential/*.py`.
- Produces: exact Git attribute rules that GitHub Linguist applies after commit; a Go regression test that rejects missing, widened, or misleading rules.

- [ ] **Step 1: Add the failing Go contract test before changing `.gitattributes`**

  Add `TestGoPrimaryMonorepoLinguistPolicy` to `tools/release/workflow_test.go`. It must read `.gitattributes` from the repository root and require these full lines:

  ```go
  required := []string{
      "evaluation/** -linguist-detectable",
      "integrations/** -linguist-detectable",
      "test/conformance/*.py -linguist-detectable",
      "test/differential/*.py -linguist-detectable",
  }
  forbidden := []string{
      "linguist-vendored",
      "linguist-generated",
  }
  ```

  The test must fail with the missing exact rule or forbidden attribute. It must not accept an arbitrary `*.py` global rule because that could hide Go-adjacent Python fixtures outside the reviewed roots.

- [ ] **Step 2: Run the new test and record the expected failure**

  Run:

  ```powershell
  go test ./tools/release -run TestGoPrimaryMonorepoLinguistPolicy -count=1
  ```

  Expected: FAIL because no `linguist-detectable` rules exist yet.

- [ ] **Step 3: Add only the four reviewed Linguist rules**

  Append this explanatory block to `.gitattributes`, keeping the existing LF and binary entries unchanged:

  ```gitattributes
  # Maintained auxiliary monorepo assets. They remain tested and licensed, but
  # are not implementation languages of the PowerContext Go primary product.
  evaluation/** -linguist-detectable
  integrations/** -linguist-detectable
  test/conformance/*.py -linguist-detectable
  test/differential/*.py -linguist-detectable
  ```

- [ ] **Step 4: Prove the checked-out attribute semantics and the regression test**

  Run:

  ```powershell
  git check-attr linguist-detectable -- evaluation/src/powercontext_eval/runner.py integrations/codex/plugins/powercontext/hooks/recall.py test/conformance/python_oracle_fixture.py test/differential/compare_servers.py
  go test ./tools/release -run TestGoPrimaryMonorepoLinguistPolicy -count=1
  git diff --check
  ```

  Expected: each listed path reports `linguist-detectable: unset`; the Go contract passes; whitespace validation passes.

- [ ] **Step 5: Commit the isolated language-policy slice**

  ```powershell
  git add .gitattributes tools/release/workflow_test.go
  git commit -m "build: mark auxiliary monorepo sources non-detectable"
  ```

### Task 2: Split pre-WP6 and retained-adapter CI contracts

**Files:**
- Modify: `.github/workflows/migration-gates.yml`
- Modify: `tools/release/workflow_test.go`
- Test: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: the existing `host-adapters` job and all current adapter commands.
- Produces: stable `host-adapters` job ID for pre-WP6 required coverage, new `retained-host-adapters` job ID for P3 evidence, and independently sanitized failure diagnostics for each job.

- [ ] **Step 1: Write workflow-contract tests that fail against the mixed job**

  Replace the single mixed-job assertions in `TestHostAdapterFailureDiagnosticsAreBoundedAndSanitized` with two focused checks, or add two new tests named `TestPreWP6HostAdapterWorkflowContract` and `TestRetainedHostAdapterWorkflowContract`.

  The pre-WP6 test must require:

  ```go
  preWP6, ok := workflow.Jobs["host-adapters"]
  if !ok {
      t.Fatal("migration-gates.yml has no host-adapters job")
  }
  if preWP6.Name != "Pre-WP6 host adapters" {
      t.Fatalf("host-adapters name = %q", preWP6.Name)
  }
  ```

  It must assert that the Python command contains the Codex and WorkBuddy test paths and does not contain `bub`, `claude-code`, `hermes`, or `langgraph`; it must require diagnostics with the Codex `uv.lock` plus WorkBuddy `powercontext.json.example` and `workbuddy_powercontext_hook.py` identities.

  The retained test must require `workflow.Jobs["retained-host-adapters"]`, display name `Post-WP6 retained host adapters`, all former non-Codex/WorkBuddy Python and Node commands, `if: failure()` upload behavior, and no secret-revealing commands in its diagnostics.

- [ ] **Step 2: Run the focused contract tests and record their failure**

  Run:

  ```powershell
  go test ./tools/release -run 'Test(PreWP6|Retained)HostAdapterWorkflowContract' -count=1
  ```

  Expected: FAIL because the existing workflow has only the mixed `host-adapters` job.

- [ ] **Step 3: Split `.github/workflows/migration-gates.yml` without weakening real commands**

  Keep `host-adapters:` and set `name: Pre-WP6 host adapters`. Its setup must retain Python 3.12 and uv, then execute exactly:

  ```bash
  uv run --project integrations/codex/plugins/powercontext --locked --with pytest \
    pytest integrations/codex/tests
  uv run --with pytest pytest integrations/workbuddy/tests
  ```

  Add `retained-host-adapters:` with `name: Post-WP6 retained host adapters`. Move, unchanged, the Bub, Claude Code, Hermes, LangGraph, DSH, Pi, OpenCode, and OpenClaw setup/build/test/generated-diff commands into that job. Give it its own bounded summary directory, `if: always()` summary step, and `if: failure()` artifact upload; do not use `continue-on-error`.

  In the pre-WP6 summary, hash:

  ```bash
  sha256sum \
    integrations/codex/plugins/powercontext/uv.lock \
    integrations/workbuddy/plugins/powercontext/powercontext.json.example \
    integrations/workbuddy/plugins/powercontext/hooks/workbuddy_powercontext_hook.py
  ```

- [ ] **Step 4: Update workflow-contract expectations and validate static workflow behavior**

  Update `TestContinuousIntegrationPreservesPythonTopologyAndGoAssurance` so `migration-gates.yml` requires both visible job names. Run:

  ```powershell
  go test ./tools/release -run 'Test(PreWP6|Retained|HostAdapter|ContinuousIntegration)' -count=1
  make actionlint
  git diff --check
  ```

  Expected: all selected tests pass; actionlint reports no invalid job, cache, expression, shell, or action configuration; diff check passes.

- [ ] **Step 5: Commit the CI-scope slice**

  ```powershell
  git add .github/workflows/migration-gates.yml tools/release/workflow_test.go
  git commit -m "ci: split pre-WP6 and retained adapter gates"
  ```

### Task 3: Align the repository documentation and issue work plan

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture/README.md`
- Modify: `docs/release/INSTALL.md`
- Modify: `.github/workflows/README.md`
- Modify: GitHub Issue `ob-labs/powercontext-go#3`
- Test: documentation build and Issue readback

**Interfaces:**
- Consumes: the committed classification rules and CI job names from Tasks 1–2.
- Produces: one consistent user-facing description of the Go-primary monorepo and an Issue #3 checklist that does not widen WP6 acceptance.

- [ ] **Step 1: Update the root and architecture descriptions**

  In `README.md`, replace the undifferentiated integrations bullet with language that identifies `integrations/` and `evaluation/` as maintained auxiliary monorepo assets, says they communicate with or evaluate the Go Server, and limits the pre-WP6 primary acceptance matrix to Codex, WorkBuddy, and SQLite.

  In `docs/architecture/README.md`, keep every existing directory in the tree but annotate `evaluation/` as a deployment-neutral Codex/SQLite evaluation control plane and `integrations/` as host-native assets. Add a short ownership paragraph stating that retained adapters other than Codex/WorkBuddy are P3 assets, not WP6 acceptance evidence.

- [ ] **Step 2: Update installation and workflow documentation**

  In `docs/release/INSTALL.md`, retain the statement that the Go binary has no Python runtime dependency. Follow it with this exact distinction in prose: Python and TypeScript assets are tracked in the monorepo for host-native integrations and evaluation; they are not Go binary runtime requirements; only Codex, WorkBuddy, and SQLite form the pre-WP6 support matrix.

  In `.github/workflows/README.md`, replace the one-row description of mixed host adapters/evaluation with three entries: `Pre-WP6 host adapters`, `Post-WP6 retained host adapters`, and `Codex/SQLite evaluation control plane`. State that required-check branch protection applies to the first and not the retained P3 job; administrators must make that branch-protection adjustment after the job appears on the merged default branch.

- [ ] **Step 3: Add the Issue #3 monorepo-boundary work package without reopening completed work**

  Download the current Issue body immediately before editing:

  ```powershell
  $issueBodyPath = Join-Path $env:TEMP 'powercontext-go-issue3-monorepo-boundary.md'
  gh issue view 3 --repo ob-labs/powercontext-go --json body,updatedAt | ConvertFrom-Json
  ```

  Insert a `P0 — Go-primary monorepo boundary` subsection before the WP5/WP6 acceptance priority. Its unchecked work items must cover: exact `.gitattributes` classifications; required Codex/WorkBuddy workflow; separate retained-host workflow; documentation consistency; post-merge language API verification; and branch-protection review. Its prose must state that all source remains in one repository, retained adapters remain P3, and seekDB/OceanBase remain final P4.

  Submit only after comparing checked-item counts before and after:

  ```powershell
  gh issue edit 3 --repo ob-labs/powercontext-go --body-file $issueBodyPath
  gh issue view 3 --repo ob-labs/powercontext-go --json body,updatedAt
  ```

  Use a temporary file outside the repository or delete it with `apply_patch` after submission. Do not include secrets, local paths, or stale check counts.

- [ ] **Step 4: Validate documentation and review the exact Issue result**

  Run:

  ```powershell
  make docs-test
  git diff --check
  gh issue view 3 --repo ob-labs/powercontext-go --json body,updatedAt,url
  ```

  Expected: documentation build succeeds; no whitespace errors; Issue readback contains the monorepo P0 items, Codex/WorkBuddy/SQLite pre-WP6 scope, P3 retained agents, and final P4 seekDB/OceanBase boundary.

- [ ] **Step 5: Commit the repository-documentation slice**

  ```powershell
  git add README.md docs/architecture/README.md docs/release/INSTALL.md .github/workflows/README.md
  git commit -m "docs: define Go-primary monorepo scope"
  ```

### Task 4: Prove the branch and default-branch result

**Files:**
- Verify: `.gitattributes`
- Verify: `.github/workflows/migration-gates.yml`
- Verify: `tools/release/workflow_test.go`
- Verify: documentation files and GitHub Issue #3

**Interfaces:**
- Consumes: all three implementation commits and the live GitHub repository.
- Produces: exact-Head verification evidence, a reviewable pull request, merged default-branch confirmation, and post-Linguist language-API evidence.

- [ ] **Step 1: Run the focused local acceptance set on the final branch Head**

  Run:

  ```powershell
  go test ./tools/release -count=1
  make actionlint
  make docs-test
  git diff --check
  git status --short
  ```

  Expected: every command succeeds and `git status --short` is empty.

- [ ] **Step 2: Push the branch and open a focused pull request**

  Run:

  ```powershell
  $prBodyPath = Join-Path $env:TEMP 'powercontext-go-monorepo-boundary-pr.md'
  git push -u origin codex/monorepo-boundary
  gh pr create --repo ob-labs/powercontext-go --base main --head codex/monorepo-boundary --title "ci: define Go-primary monorepo boundary" --body-file $prBodyPath
  ```

  The PR description must link Issue #3, name the four Linguist paths, explain that no maintained code is labeled vendored/generated, list the pre-WP6 versus P3 job identities, state that no adapter/evaluation source is deleted, and record the exact validation commands.

- [ ] **Step 3: Recheck exact Head and CI before merge**

  Read the PR base/head SHA, files, review state, and check conclusions after the last push. Do not reuse evidence if the head changes. Resolve any failing gate without weakening the contract tests or making retained adapters silently pass.

- [ ] **Step 4: Merge only after live approval and then verify default branch**

  After the repository's normal review approval, merge through the configured GitHub policy. Verify server-side merge metadata, fetch `origin/main`, and confirm the merged commit and Issue state. A zero exit code from a merge command is not sufficient proof.

- [ ] **Step 5: Verify Linguist after default-branch recalculation**

  Poll no more often than once per hour until GitHub reflects the committed `.gitattributes`:

  ```powershell
  gh api repos/ob-labs/powercontext-go/languages
  ```

  Expected: the classified `evaluation/`, `integrations/`, and Python fixture paths are absent from the language-byte total and Go is the leading reported language. Record the full API response and the merge SHA in Issue #3 or the PR evidence; do not claim the percentage changed before this API confirms it.

## Plan self-review

### Spec coverage

- Single-repository preservation and no adapter deletion: Tasks 1–3.
- Honest Linguist classification and GitHub API verification: Tasks 1 and 4.
- Codex/WorkBuddy/SQLite-only pre-WP6 CI: Task 2.
- Executable P3 retained-adapter evidence: Task 2.
- Evaluation's Codex/SQLite role and documentation: Task 3.
- Issue #3 P3/P4 sequencing: Task 3.
- Exact-Head, merge, and default-branch proof: Task 4.

### Completeness scan

The plan names exact files, commands, expected outcomes, and commit boundaries for every task. It has no deferred-action markers or generic test instructions.

### Type and contract consistency

The workflow retains the stable `host-adapters` job ID for branch protection. The separate P3 job is consistently named `retained-host-adapters`. Documentation and Issue text use the same pre-WP6 boundary: Codex, WorkBuddy, and SQLite; retained agents are P3; seekDB/OceanBase are P4.
