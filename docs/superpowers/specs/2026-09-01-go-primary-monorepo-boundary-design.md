# Go-primary monorepo boundary design

## Status

Approved design direction: retain one `powercontext-go` repository while
making the Go Server, SDK, CLI, OpenAPI contract, and SQLite path its primary
product surface. This document defines the boundary before the corresponding
Issue, documentation, CI, and GitHub language-statistics changes are made.

## Problem

GitHub currently reports Python as 27.37% of this repository. The number is
accurate for tracked source bytes: the `evaluation/` Python control plane is
about 1.52 MB, host-native Python adapters are about 0.41 MB, and Python
conformance/differential fixtures are about 0.07 MB. The repository therefore
looks like a broad Python/TypeScript monorepo even though its principal
deliverable is the Go Server, Go SDK, and Go CLI.

The existing CI also treats every retained adapter as one `Host adapters` job.
That contradicts the accepted delivery order: before WP6, only Codex and
WorkBuddy are in the host-agent acceptance scope, and SQLite is the only
database in scope. OpenCode, DSH, LangChain, Pydantic AI, Hermes, and other
retained adapters belong to the post-WP6 expansion; seekDB and OceanBase are
the final backend scope.

## Goals

1. Keep all current source, test evidence, and adapter history in this single
   repository.
2. Make the root documentation and Issue #3 describe a Go-primary monorepo
   rather than a repository whose every language is a release-equivalent
   product surface.
3. Make GitHub language statistics identify the Go primary product without
   misclassifying self-maintained sources as vendored or generated.
4. Make pre-WP6 CI acceptance explicitly cover only Codex, WorkBuddy, and
   SQLite, while retaining executable post-WP6 adapter evidence in an
   independently named, non-pre-WP6 gate.
5. Preserve the Python-to-Go conformance and differential fixtures needed to
   prove the pinned PowerContext v0.1.0 contract.

## Non-goals

- Do not delete or move `evaluation/`, `integrations/`, or Python fixtures to
  another repository.
- Do not rewrite host adapters into Go.
- Do not label maintained code with `linguist-vendored` or
  `linguist-generated`.
- Do not make a post-WP6 adapter failure invisible by weakening assertions,
  silently skipping it, or treating a non-run job as a successful gate.
- Do not move seekDB or OceanBase into the pre-WP6 database acceptance scope.

## Repository ownership model

| Area | Ownership and release role | Pre-WP6 acceptance role |
| --- | --- | --- |
| Go domain packages, `internal/`, `client/`, `server/`, `cmd/`, `openapi/`, `api/v1/` | Primary PowerContext Go implementation and release surface | Required |
| SQLite runtime and SQLite tests | Default self-contained persistence implementation | Required |
| `integrations/codex/`, `integrations/workbuddy/` | Maintained host-native assets consumed by the Go product | Required host adapters |
| `evaluation/` | Maintained, deployment-neutral Codex/SQLite evaluation control plane; not embedded in the Go binary or release archive | WP5 evaluation evidence only |
| Other `integrations/**` directories | Maintained retained-host assets and historical evidence | Post-WP6 only |
| `test/conformance/*.py`, `test/differential/*.py` | Python execution fixtures used to prove Python-to-Go compatibility | Required parity evidence, not product implementation |
| seekDB and OceanBase code/tests | Optional/final storage-backend work | Final P4 only |

The root repository remains a monorepo. "Primary" describes the release and
acceptance boundary; it does not claim the excluded directories are unowned.

## GitHub language-statistics policy

The root `.gitattributes` must retain its existing LF and binary rules and add
these explicit Linguist classifications:

```gitattributes
# Tracked, maintained auxiliary assets; not implementation languages of the
# PowerContext Go primary product.
evaluation/** -linguist-detectable
integrations/** -linguist-detectable
test/conformance/*.py -linguist-detectable
test/differential/*.py -linguist-detectable
```

`-linguist-detectable` changes only GitHub language classification. It must
not change checkout attributes, syntax highlighting intent, test execution,
license scanning, release contents, generated-file checks, or CI paths.
GitHub recalculates language statistics from a committed `.gitattributes`;
the change is accepted only after the default-branch language API confirms the
expected Go-primary result.

## CI topology

The current single `host-adapters` job mixes pre-WP6 and deferred products.
It must be split without path-filter disappearance or test weakening.

1. The existing required job identity stays stable as `host-adapters`, with
   display name `Pre-WP6 host adapters`. It executes only Codex and WorkBuddy
   installation, packaging, surface, service-chain, redaction, and transport
   tests. Its diagnostics contain only those two adapter outcomes plus one
   stable dependency or source identity per adapter: the Codex `uv.lock` hash
   and hashes of the WorkBuddy configuration template and hook entrypoint.
2. A separately named `retained-host-adapters` job runs the currently retained
   non-pre-WP6 adapters with their real Python/Node install, generation,
   type-check, unit, build, and call-through commands. It remains a failing,
   evidence-producing job when run; branch-protection configuration must not
   count it as a pre-WP6 required check.
3. The evaluation job is renamed to `Codex/SQLite evaluation control plane`.
   It keeps its current Python, frontend, and deterministic evaluation tests,
   but its documentation and diagnostics identify it as WP5 Codex/SQLite
   evidence rather than proof of every host adapter.
4. Workflow documentation describes the three distinct contracts above and
   names their required/non-required lifecycle. The job graph must continue to
   produce one stable conclusion for every pull request; no path filter may
   omit a required pre-WP6 job.

## Documentation and Issue contract

Issue #3 gains a monorepo-boundary work package before WP6 acceptance. It
records the exact directory classifications, the GitHub-statistics policy, the
CI split, and the validation requirements. Its existing completed checkboxes
remain completed. The WorkBuddy/Codex plus SQLite limit remains the sole
pre-WP6 acceptance scope. All retained hosts stay in P3, and seekDB/OceanBase
stay in final P4.

The README, architecture overview, release installation guide, and workflow
README must state all of the following consistently:

- The Go binary does not need Python at runtime.
- The repository deliberately tracks host-native adapters and a Python
  evaluation control plane, but they are auxiliary monorepo assets rather than
  Go core implementation languages.
- Before WP6 acceptance, only Codex, WorkBuddy, and SQLite are supported by
  the primary acceptance matrix.
- Other retained adapters are not removed; they have an explicit post-WP6
  ownership and CI path.

## Migration sequence and rollback

1. Add the Issue #3 work package and this documented boundary.
2. Add `.gitattributes` classifications and a repository test that asserts the
   exact classification lines, preserving existing LF rules.
3. Split the host-adapter workflow and update its diagnostics and workflow
   documentation.
4. Update root/release/architecture documentation and test its executable
   commands/links.
5. Push the branch, wait for exact-Head CI, merge, then query the default
   branch's GitHub language API after Linguist recalculates.

Rollback is a normal revert of this focused boundary change. It does not
delete adapters, evaluation data, test fixtures, lock files, or release
artifacts. If a required Codex/WorkBuddy test is missing from the split, the
split is repaired before merge; it is never bypassed by expanding the job back
to every retained adapter.

## Acceptance criteria

1. The committed `.gitattributes` contains the four exact
   `-linguist-detectable` path rules and no `linguist-vendored` or
   `linguist-generated` rule for maintained project paths.
2. GitHub's default-branch language API no longer counts the classified
   auxiliary paths and reports Go as the primary language.
3. The required `host-adapters` job runs real Codex and WorkBuddy tests only;
   its failure diagnostics include both outcomes, the Codex lock identity, and
   the WorkBuddy template and hook identities.
4. The separate retained-adapter job remains executable and failing when its
   own real test or generated-diff command fails, but is documented as
   post-WP6 evidence rather than a pre-WP6 completion condition.
5. The evaluation job continues to run the Python control-plane and frontend
   tests and is documented as Codex/SQLite WP5 evidence.
6. README, architecture, release, workflow documentation, and Issue #3 agree
   on the same repository and phase boundaries.
7. `make docs-test`, workflow/YAML contract tests, and the relevant adapter
   test commands succeed on the exact branch Head without leaving worktree
   changes.
