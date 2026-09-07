# Supply-Chain and Reliability Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add continuous supply-chain review, complete dependency-update coverage, scheduled reliability evidence, and signed provenance for every published PowerContext artifact and image.

**Architecture:** Keep policy in GitHub-native YAML and enforce it through the existing Go governance and workflow contract tests. Release jobs attest final bytes and image digests at their actual producer boundaries; the release verifier consumes those attestations before any executable surface runs.

**Tech Stack:** Go 1.27, GitHub Actions, Dependabot, OpenSSF Scorecard, GitHub Artifact Attestations, Go fuzzing/race detector, YAML v2, jq, GitHub CLI.

**Spec:** `docs/superpowers/specs/2026-09-07-supply-chain-reliability-hardening-design.md`

## Global Constraints

- Preserve existing BuildKit `sbom: true` and `provenance: mode=max` settings.
- Explicitly select `google.golang.org/grpc v1.83.1` or newer in both owned Go module manifests.
- Cover every tracked gomod, npm/pnpm, uv, Docker, and GitHub Actions manifest directory.
- Pin every third-party GitHub Action to a reviewed 40-character commit SHA.
- Do not change product APIs, OpenAPI, persistence, runtime orchestration, branch rulesets, or unsupported-backend policy.
- Do not publish a release or image from the pull request.
- Keep reliability logs bounded and free of protected product content or credentials.
- Treat exact-Head GitHub CI as the authoritative full Linux release-test evidence; do not weaken known Windows executable-mode or WSL environment failures.

---

### Task 1: Complete Dependabot Coverage

**Files:**
- Modify: `.github/dependabot.yml`
- Modify: `tools/governance-check/main.go`
- Modify: `tools/governance-check/main_test.go`

**Interfaces:**
- Consumes: strict YAML decoding through `yaml.UnmarshalStrict`.
- Produces: exact ecosystem and manifest-directory validation through `checkDependabotUpdate`.

- [ ] **Step 1: Load Modern Go guidance**

```powershell
& 'C:\Users\alex\.codex\skills\use-modern-go\scripts\run-tool.ps1' list --file-path tools/governance-check/main.go
& 'C:\Users\alex\.codex\skills\use-modern-go\scripts\run-tool.ps1' list --file-path tools/governance-check/main_test.go
```

Expected: complete, unfiltered Go 1.27 guidance for both files.

- [ ] **Step 2: Add failing governance fixtures**

Extend `TestCheckRepositoryEnforcesGovernanceContract` with mutants that remove `/test/downstream`, add an unowned directory, duplicate one directory, set both `directory` and `directories`, change the uv day, and remove the npm group. Update `validDependabotConfig` to describe all five approved ecosystems so the valid fixture expresses the final contract.

- [ ] **Step 3: Prove the implementation is missing**

```powershell
go test -count=1 ./tools/governance-check -run '^TestCheckRepositoryEnforcesGovernanceContract$'
```

Expected: FAIL because the current parser does not support `directories` and expects only two root entries.

- [ ] **Step 4: Implement exact directory-set validation**

Use these structures:

```go
type dependabotUpdate struct {
	PackageEcosystem      string                     `yaml:"package-ecosystem"`
	Directory             string                     `yaml:"directory"`
	Directories           []string                   `yaml:"directories"`
	Schedule              dependabotSchedule         `yaml:"schedule"`
	OpenPullRequestsLimit int                        `yaml:"open-pull-requests-limit"`
	Groups                map[string]dependabotGroup `yaml:"groups"`
}

type dependabotExpectation struct {
	Ecosystem   string
	Directories []string
	Day         string
	Group       string
}
```

Reject simultaneous `directory` and `directories`, empty lists, duplicates, missing directories, and extra directories. Compare sorted clones with `slices.Equal`.

- [ ] **Step 5: Configure the exact policy**

```go
[]dependabotExpectation{
	{Ecosystem: "gomod", Directories: []string{"/", "/test/downstream"}, Day: "monday", Group: "go-minor-patch"},
	{Ecosystem: "github-actions", Directories: []string{"/"}, Day: "tuesday", Group: "actions-minor-patch"},
	{Ecosystem: "uv", Directories: []string{"/evaluation", "/integrations/bub", "/integrations/codex/plugins/powercontext", "/integrations/langchain", "/integrations/langgraph", "/integrations/pydantic-ai", "/tools/docs"}, Day: "wednesday", Group: "uv-minor-patch"},
	{Ecosystem: "npm", Directories: []string{"/evaluation/web", "/integrations/dsh/plugins/powercontext", "/integrations/openclaw/plugins/memory-powercontext", "/integrations/opencode/plugins/powercontext", "/integrations/pi/plugins/powercontext"}, Day: "thursday", Group: "npm-minor-patch"},
	{Ecosystem: "docker", Directories: []string{"/"}, Day: "friday", Group: "docker-minor-patch"},
}
```

Use `directories` for gomod, uv, and npm in `.github/dependabot.yml`; use root `directory` for Actions and Docker. Each entry uses weekly scheduling, limit 4, and one group containing only minor and patch updates.

- [ ] **Step 6: Run focused green tests**

```powershell
go test -count=1 ./tools/governance-check
go run ./tools/governance-check
```

Expected: PASS and exit 0.

- [ ] **Step 7: Commit**

```powershell
git add .github/dependabot.yml tools/governance-check/main.go tools/governance-check/main_test.go
git commit -m "ci: cover all dependency manifests"
```

---

### Task 2: Resolve the gRPC-Go Advisory in Both Modules

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `test/downstream/go.mod`
- Modify: `test/downstream/go.sum`

**Interfaces:**
- Consumes: owned-module inventory and downstream replace directive.
- Produces: explicit indirect `google.golang.org/grpc v1.83.1` selection in both module roots.

- [ ] **Step 1: Capture the vulnerable baseline**

```powershell
go list -m -f '{{.Path}} {{.Version}}' google.golang.org/grpc
go -C test/downstream list -m -f '{{.Path}} {{.Version}}' google.golang.org/grpc
```

Expected before edits: both resolve `v1.82.1`.

- [ ] **Step 2: Upgrade both module roots**

```powershell
go get google.golang.org/grpc@v1.83.1
go mod tidy
go -C test/downstream get google.golang.org/grpc@v1.83.1
go -C test/downstream mod tidy
```

Inspect the diff and retain the explicit indirect requirement in both `go.mod` files. Retain only checksum or transitive changes required by the selection.

- [ ] **Step 3: Verify readonly module state**

```powershell
go list -m -f '{{.Version}}' google.golang.org/grpc
go -C test/downstream list -m -f '{{.Version}}' google.golang.org/grpc
go mod tidy -diff
go mod verify
go -C test/downstream mod tidy -diff
go -C test/downstream mod verify
```

Expected: both report `v1.83.1` or newer, tidy has no diff, and verification succeeds.

- [ ] **Step 4: Run changed-risk tests**

```powershell
go test -count=1 ./test/downstream
```

Then run `make dependency-security` and `make downstream-compat` in a supported shell. Preserve environment-only failures for exact-Head CI rather than weakening the commands.

- [ ] **Step 5: Commit**

```powershell
git add go.mod go.sum test/downstream/go.mod test/downstream/go.sum
git commit -m "fix(deps): select patched grpc-go"
```

---

### Task 3: Add Continuous Scorecard Analysis

**Files:**
- Create: `.github/workflows/scorecard.yml`
- Modify: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: existing YAML workflow test types and pinned-action convention.
- Produces: advisory SARIF analysis with least-privilege permissions.

- [ ] **Step 1: Load Modern Go guidance**

```powershell
& 'C:\Users\alex\.codex\skills\use-modern-go\scripts\run-tool.ps1' list --file-path tools/release/workflow_test.go
```

- [ ] **Step 2: Write a failing Scorecard contract**

Add `TestScorecardWorkflowContract` requiring default-branch push, branch-protection, weekly schedule, manual dispatch, empty top-level permissions, and job permissions `contents: read`, `security-events: write`, `id-token: write`. Require `results.sarif`, five-day artifact retention, code-scanning upload, and `publish_results: true`.

Require exact action identities:

```text
ossf/scorecard-action@2d1146689b8cda280b9bc96326124645441f03bc
actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a
github/codeql-action/upload-sarif@cdf488f595d80d6e07e03d4674febd5ab45fa938
```

Add mutants for missing `id-token`, mutable action tag, missing SARIF upload, and disabled result publication.

- [ ] **Step 3: Prove red, implement, and prove green**

```powershell
go test -count=1 ./tools/release -run '^TestScorecardWorkflowContract$'
```

Expected before workflow creation: FAIL. Create `.github/workflows/scorecard.yml` with a 15-minute analysis job and the required pinned actions, then rerun and expect PASS.

- [ ] **Step 4: Commit**

```powershell
git add .github/workflows/scorecard.yml tools/release/workflow_test.go
git commit -m "ci: add continuous scorecard analysis"
```

---

### Task 4: Add Nightly Reliability Evidence

**Files:**
- Create: `.github/workflows/nightly-reliability.yml`
- Modify: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: five existing fuzz targets and four supported high-risk packages.
- Produces: stable `Fuzzing` status and bounded repeated-test statistics.

- [ ] **Step 1: Write failing structural and mutant tests**

Add `TestNightlyReliabilityWorkflowContract` requiring schedule plus manual dispatch only, read-only permissions, `fail-fast: false`, the exact five fuzz target/package pairs, `-fuzztime=4m`, failure-only corpus upload, and an `if: always()` aggregate that fails for non-success matrix results.

Require the stability job to contain the complete operation:

```text
go test -json -race -shuffle=on -count=25 ./internal/runtime ./internal/sqlstore ./source ./server
```

It must preserve `${PIPESTATUS[0]}`, always upload a capped summary, and upload raw JSONL only on failure. Add mutants for missing target, 3-second duration, fail-fast true, missing race/count, lost pipe status, unconditional raw logs, and missing cancellation handling.

- [ ] **Step 2: Prove red, implement, and prove green**

```powershell
go test -count=1 ./tools/release -run '^TestNightlyReliabilityWorkflowContract$'
```

Expected before creation: FAIL. Create the workflow with a non-fail-fast fuzz matrix and four-minute runs. The stability step writes `go test -json` through `tee`, captures `${PIPESTATUS[0]}`, uses `jq` to sort and cap per-test counts at 200 rows, records omitted rows, always emits a Markdown summary, and returns the real test status. Rerun and expect PASS.

- [ ] **Step 3: Commit**

```powershell
git add .github/workflows/nightly-reliability.yml tools/release/workflow_test.go
git commit -m "ci: add nightly reliability evidence"
```

---

### Task 5: Attest Release Artifacts and OCI Images

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: binary `dist/*`, image build digests, aggregate `SHA256SUMS`, and `IMAGE-DIGESTS.json`.
- Produces: GitHub attestations from actual producer jobs and `github_provenance_attestations: true` release metadata.

- [ ] **Step 1: Add failing provenance mutants**

Require exact action identity:

```text
actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8
```

Require `attestations: write` and `id-token: write` only on producer jobs; artifact attestation after checksum creation and before upload; separate image attestations after each build using its exact digest, tag-free subject name, and `push-to-registry: true`; stage attestation after aggregate files are final and before release create/upload. Add mutants for each boundary.

- [ ] **Step 2: Prove the current release workflow fails the new contract**

```powershell
go test -count=1 ./tools/release -run 'TestReleaseWorkflow|TestReleaseImage'
```

- [ ] **Step 3: Add producer-side attestations**

Give binary producer jobs `contents: read`, `id-token: write`, and `attestations: write`; attest `dist/*` before upload. Add the two write permissions to the image job, expose tag-free image subject names, and attest standard/full image digests independently with registry publication. Add the same permissions to the stage job, set `github_provenance_attestations: true`, and attest final aggregate files before release mutation.

- [ ] **Step 4: Prove green and commit**

```powershell
go test -count=1 ./tools/release -run 'TestReleaseWorkflow|TestReleaseImage'
git add .github/workflows/release.yml tools/release/workflow_test.go
git commit -m "ci: attest release artifacts and images"
```

---

### Task 6: Verify Provenance Before Execution

**Files:**
- Modify: `.github/workflows/release-verify.yml`
- Modify: `tools/release/workflow_test.go`

**Interfaces:**
- Consumes: release assets and immutable image references produced by Task 5.
- Produces: repository-bound attestation verification before extraction or execution.

- [ ] **Step 1: Verify current GitHub CLI syntax**

```powershell
gh attestation verify --help
```

Use the current supported `oci://<image>@<digest>` form if present in official help; do not infer syntax.

- [ ] **Step 2: Add failing verification-order mutants**

Require `gh attestation verify` with `--repo "$GITHUB_REPOSITORY"` for every archive, SPDX document, platform checksum, aggregate checksum, and image digest record. Require both immutable OCI references to be verified before Buildx inspection, `docker pull`, or `docker run`. Add mutants for omitted asset class, missing repository binding, mutable image reference, verification after extraction, and verification after execution.

- [ ] **Step 3: Prove red, implement, and prove green**

```powershell
go test -count=1 ./tools/release -run '^TestReleaseVerifyWorkflow'
```

Expected before modification: FAIL. After checksums, enumerate exact expected subjects and verify each before extraction. Require `.github_provenance_attestations == true`, validate digest-shaped image references, and verify both OCI subjects before inspection. Rerun and expect PASS.

- [ ] **Step 4: Commit**

```powershell
git add .github/workflows/release-verify.yml tools/release/workflow_test.go
git commit -m "ci: verify signed release provenance"
```

---

### Task 7: Document and Enforce the New Contracts

**Files:**
- Modify: `.github/workflows/README.md`
- Modify: `docs/release/POLICY.md`
- Modify: `AGENTS.md`
- Modify: `tools/governance-check/main.go`
- Modify: `tools/governance-check/main_test.go`

**Interfaces:**
- Consumes: final workflow behavior.
- Produces: operator documentation, release policy, and one learned prevention invariant.

- [ ] **Step 1: Add a failing documentation contract**

Extend `checkReleasePolicy` and the test fixture to require:

```text
signed build provenance
immutable artifact digest
verify the attestation before execution
```

Add a mutant removing the final phrase and expect governance-check failure.

- [ ] **Step 2: Prove red, update docs, and prove green**

```powershell
go test -count=1 ./tools/governance-check -run '^TestCheckRepositoryEnforcesGovernanceContract$'
```

Update the workflow table with Scorecard and nightly reliability. Document advisory Scorecard results, bounded nightly evidence, retained BuildKit provenance, signed final artifacts/digests, and pre-execution verification. Add or merge this learned rule in `AGENTS.md`:

```text
When a release workflow publishes a downloadable artifact or OCI image, create signed provenance in the job that produced the final bytes or digest and bind it to the repository and immutable release identity. Verify every attestation before extraction or execution, and prove missing permissions, incorrect subjects, mutable image references, and reversed verification order with workflow-contract mutants.
```

Rerun the test and expect PASS.

- [ ] **Step 3: Commit**

```powershell
git add .github/workflows/README.md docs/release/POLICY.md AGENTS.md tools/governance-check/main.go tools/governance-check/main_test.go
git commit -m "docs: define signed provenance policy"
```

---

### Task 8: Verify and Open the Pull Request

**Files:**
- Modify only if a targeted gate identifies an in-scope defect.

**Interfaces:**
- Consumes: Tasks 1-7.
- Produces: one pushed branch and one exact-Head pull request; no merge.

- [ ] **Step 1: Format and run focused tests**

```powershell
gofmt -w tools/governance-check/main.go tools/governance-check/main_test.go tools/release/workflow_test.go
go test -count=1 ./tools/governance-check
go test -count=1 ./tools/release -run 'TestScorecardWorkflowContract|TestNightlyReliabilityWorkflowContract|TestReleaseWorkflow|TestReleaseVerifyWorkflow'
```

Expected: PASS.

- [ ] **Step 2: Validate workflows, generators, and modules**

```text
make governance-check
make actionlint
make check-generated
make module-integrity
make dependency-security
make downstream-compat
make lint
```

Run from a supported shell. Preserve local environment gaps instead of changing tests, assertions, CGO, or build tags.

- [ ] **Step 3: Recheck module selection and diff hygiene**

```powershell
go mod tidy -diff
go mod verify
go -C test/downstream mod tidy -diff
go -C test/downstream mod verify
go list -m -f '{{.Version}}' google.golang.org/grpc
go -C test/downstream list -m -f '{{.Version}}' google.golang.org/grpc
git diff --check origin/main...HEAD
git status --short
git diff --stat origin/main...HEAD
git log --oneline origin/main..HEAD
```

Expected: clean manifests, both gRPC selections at least `v1.83.1`, approved files only, and no temporary artifacts.

- [ ] **Step 4: Push and create one PR**

```powershell
git push -u origin codex/quality-supply-chain-hardening
gh pr create --repo ob-labs/powercontext-go --base main --head codex/quality-supply-chain-hardening --title "ci: harden supply-chain and reliability evidence" --body-file .workbuddy/pr-body.md
```

The reviewed PR body must record exact commands and outcomes, local environment-only gaps, and that production attestation E2E awaits a controlled Release run. Remove the temporary body file after PR creation if it is untracked.

- [ ] **Step 5: Reconcile exact-Head CI**

Refresh the PR Head SHA and status rollup, wait for all workflows, and report every failure, skip, or pending job separately. Fix in-scope failures on the same branch and repeat exact-Head verification. Do not merge.
