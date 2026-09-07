# Supply-Chain and Reliability Hardening Design

Status: approved design

## Objective

Close the remaining repository-quality gaps without changing product APIs or
runtime behavior:

- run continuous OpenSSF Scorecard analysis;
- keep every tracked Go, npm/pnpm, uv, Docker, and GitHub Actions dependency
  surface under bounded Dependabot maintenance;
- resolve the current gRPC-Go advisory in both owned Go modules through an
  explicit root-manifest version selection;
- add scheduled fuzzing, repeated race-enabled tests, and bounded flaky-test
  statistics;
- sign downloadable release artifacts and published OCI image digests with
  GitHub build-provenance attestations; and
- verify those attestations before release artifacts or images are executed.

The implementation must preserve the existing BuildKit SBOM and
`provenance: mode=max` output. GitHub attestations add a signed repository and
workflow identity; they do not replace the existing OCI provenance.

## Non-goals

- Do not change the public Go API, OpenAPI contract, persistence behavior, or
  runtime orchestration.
- Do not change branch rulesets or required-status configuration in this pull
  request.
- Do not restore or advertise seekDB or OceanBase support.
- Do not add an external benchmark, coverage, fuzzing, or flaky-test service.
- Do not weaken, skip, or rewrite existing release tests to accommodate local
  Windows or WSL limitations.
- Do not publish a release or image from the pull request.

## Approach

Use GitHub-native controls and extend the repository's existing contract
validators. This keeps the change reviewable and avoids a new long-lived test
or reporting service.

The alternatives considered were a new Go command that owns repeated test
execution and an external reliability platform. A new command would add
subprocess and log-lifecycle code for a workflow-only need. An external service
would add accounts, permissions, data-retention policy, and another supply-chain
dependency. Neither is justified for the requested scope.

## Dependency update coverage

`.github/dependabot.yml` will use one bounded entry for each package ecosystem.
Entries may use GitHub's `directories` form to avoid duplicate policy blocks.

The exact owned directory sets are:

| Ecosystem | Directories |
| --- | --- |
| `gomod` | `/`, `/test/downstream` |
| `github-actions` | `/` |
| `uv` | `/evaluation`, `/integrations/bub`, `/integrations/codex/plugins/powercontext`, `/integrations/langchain`, `/integrations/langgraph`, `/integrations/pydantic-ai`, `/tools/docs` |
| `npm` | `/evaluation/web`, `/integrations/dsh/plugins/powercontext`, `/integrations/openclaw/plugins/memory-powercontext`, `/integrations/opencode/plugins/powercontext`, `/integrations/pi/plugins/powercontext` |
| `docker` | `/` |

The schedules will be staggered across weekdays. Minor and patch updates will
remain grouped with a small open-pull-request limit. Major changes will remain
individually reviewable.

`tools/governance-check` will validate the exact ecosystem and directory sets,
schedule, pull-request limit, and grouping policy. It will reject a missing,
duplicate, or additional directory, and it will reject an update entry that
uses both `directory` and `directories`. YAML decoding remains strict so an
unknown field cannot silently disable policy.

## gRPC-Go advisory resolution

Both owned Go modules will explicitly select at least
`google.golang.org/grpc v1.83.1` in their root manifests:

- `go.mod`; and
- `test/downstream/go.mod`.

The requirement remains marked indirect when no repository package imports it
directly. The explicit manifest selection is intentional security policy and
must not be represented only by checksum-file changes. Module regeneration may
retain only changes required by the new selection.

Verification will resolve the selected module version independently from each
module root, run readonly module checks, run the repository's binary
vulnerability scan, and exercise the downstream consumer contract.

## Continuous Scorecard analysis

Add `.github/workflows/scorecard.yml` with these triggers:

- default-branch pushes;
- branch-protection changes;
- a weekly schedule; and
- manual dispatch.

The workflow will start with no permissions and grant only `contents: read`,
`security-events: write`, and `id-token: write` to the analysis job. Every
third-party action will use a full commit SHA. The workflow will produce SARIF,
retain a short-lived diagnostic artifact, upload the result to code scanning,
and publish public-repository Scorecard results.

Scorecard is advisory in this change. Its numeric score will not be a required
status or a repository contract because checks are heuristic and can change
upstream. The repository contract covers the workflow identity, triggers,
permissions, pinned actions, SARIF output, and upload behavior.

## Signed release provenance

### Downloadable artifacts

Each release binary matrix job is the authoritative builder of its archives,
SPDX documents, and platform checksum manifest. After packaging and checksum
creation, and before artifact upload, that job will invoke the pinned
`actions/attest-build-provenance` action over the final `dist` subjects.

The job will add only `id-token: write` and `attestations: write` beyond its
existing read permission. Attestation will happen in the builder job so the
signed workflow identity describes the actual producer rather than a later
aggregation job.

Files created only by the release-staging job, including the aggregate checksum
manifest and immutable image-digest record, will be attested by that job after
their final bytes are produced and before the draft release is created or
updated.

### OCI images

The existing Buildx configuration will continue producing an SBOM and maximum
BuildKit provenance. After each image is pushed, the release workflow will
invoke the pinned GitHub provenance action with:

- the repository-qualified image name without a mutable tag;
- the exact digest returned by the corresponding build step; and
- registry publication enabled.

The image job will add `id-token: write` and `attestations: write` while keeping
the existing package-write permission. Standard and Full images will be
attested independently.

`IMAGE-DIGESTS.json` will record that signed GitHub provenance is required in
addition to the existing SBOM attestation field. Release verification will fail
closed when that declaration is absent or false.

## Release-side verification

`release-verify.yml` will verify downloaded artifacts in this order:

1. inventory and aggregate checksums;
2. GitHub build-provenance attestations for every downloadable subject;
3. extracted archive checksums, metadata, SPDX evidence, and integration
   inventory;
4. GitHub/registry provenance for both immutable image digests;
5. image metadata; and
6. released binary and container runtime smoke tests.

Verification will use the current supported GitHub CLI syntax and bind the
expected repository identity explicitly. A replaced artifact or image digest
must be rejected before execution. Diagnostics may identify a bounded artifact
name and verification stage, but must not expose credentials, response bodies,
or local runner paths.

## Nightly reliability evidence

Add `.github/workflows/nightly-reliability.yml` with a daily schedule and manual
dispatch. It will have read-only repository permission and an independent
concurrency group.

### Fuzzing

A non-fail-fast matrix will run the five existing high-risk fuzz targets for
four minutes each:

- `FuzzMarshalCanonicalJSONIsIdempotent`;
- `FuzzRestrictedPickleJobDecoder`;
- `FuzzAgentSkillFrontmatterParser`;
- `FuzzTruncateUTF8PreservesRuneBoundariesAndBudget`; and
- `FuzzValidJSONUnicodeNeverPanics`.

The pull-request fuzz smoke remains unchanged. A nightly failure will upload the
matching Go fuzz corpus under an artifact name containing the target and commit
identity. A stable aggregate job will fail when any matrix member fails or is
cancelled.

### Repeated concurrency and flaky-test statistics

A separate job will install the required SQLite headers and run the supported
high-risk packages with:

```text
go test -json -race -shuffle=on -count=25 \
  ./internal/runtime ./internal/sqlstore ./source ./server
```

The step will preserve the real `go test` status across output capture. Bounded
JSON-derived statistics will include the commit, Go version, platform, total
attempts, and per-test pass/fail counts. The summary will always be written to
the GitHub job summary and uploaded as a short-lived artifact. Raw JSON output
will be uploaded only on failure.

The job may continue far enough to emit its bounded statistics, but it must
return nonzero when any repeated test execution failed. It must not turn a
failure green through automatic retry.

## Contract tests

Extend `tools/governance-check/main_test.go` with valid and failing Dependabot
fixtures covering exact directories, duplicates, mutually exclusive directory
forms, schedules, limits, groups, and unknown fields.

Extend `tools/release/workflow_test.go` to parse and validate:

- the Scorecard workflow and its least-privilege pinned-action contract;
- the nightly workflow's exact fuzz inventory, duration, non-fail-fast matrix,
  failure artifacts, repeated race command, real exit-status preservation, and
  bounded summary behavior;
- release builder and staging attestation permissions, action identities,
  subject paths, and ordering;
- OCI subject-name/digest binding and registry publication; and
- release verification of every archive, SPDX document, checksum record, and
  immutable image digest before execution.

Each material boundary will have a failing mutant. Source substring checks are
not sufficient when the YAML structure or complete shell operation can be
parsed and validated.

## Documentation and learned rule

Update `.github/workflows/README.md` and the release policy documentation to
describe Scorecard, nightly reliability evidence, signed artifact provenance,
and consumer verification.

Add one concise learned rule to `AGENTS.md`: when a release workflow produces a
downloadable artifact or pushes an OCI image, the signed attestation must bind
the actual builder, final subject digest, repository identity, and immutable
release identity; release verification must validate it before execution. The
rule will require workflow-contract mutants for missing permission, incorrect
subject binding, and incorrect verification order.

## Validation

The implementation will use red-green contract tests and then run, in order:

- Modern Go guidance for every changed Go file;
- `go test -count=1 ./tools/governance-check`;
- focused `tools/release` workflow tests on Windows where supported;
- the complete `tools/release` package on a supported Linux environment or
  exact-Head CI;
- `make governance-check`;
- `make actionlint`;
- readonly module checks in both modules;
- `make module-integrity`;
- `make dependency-security`;
- `make downstream-compat`;
- `make check-generated`;
- `make lint`;
- `git diff --check`; and
- final worktree-status inspection.

The current Windows executable-mode failure and the current WSL missing-header
and linked-worktree Git limitations are baseline environment evidence. They will
not be fixed or suppressed in this pull request. GitHub exact-Head Linux CI is
the authoritative full-suite evidence.

An ordinary pull request cannot prove a release OIDC attestation end to end
without publishing artifacts. Contract tests and executable mutants will prove
the workflow configuration in the pull request. The next controlled release or
maintainer-dispatched release workflow must provide the final production-path
attestation and verification evidence.

## Expected change boundary

Expected files are limited to:

- dependency manifests and checksums;
- Dependabot configuration;
- Scorecard, nightly reliability, release, and release-verification workflows;
- workflow documentation and release policy;
- the existing governance and release workflow contract tests; and
- this design, the implementation plan, and the concise learned rule.

No generated API files, product implementation packages, persistence schemas,
or protocol contracts are in scope.
