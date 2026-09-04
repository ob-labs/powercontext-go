# Non-Backend Release Archive Closure Design

## Status

Approved by the user on 2026-09-04. This design executes only the non-backend
remainder of Issue #194 and treats safe parallel execution as the primary
delivery constraint.

## Context

Issue #194 has 30 unchecked boxes, but every Phase 5, Phase 6, and Phase 7
item exists to complete seekDB or OceanBase alignment. Those phases are
excluded from this delivery. The remaining product work is Phase 1: make the
release archives describe, contain, and prove the complete retained
integration set on current `main`.

The published `powercontext-v0.1.0` release is immutable. Current `main`
contains twelve integration roots, including LangChain, which landed after
that release, and the WorkBuddy Go-hook, which landed in PR #199. The release
packager copies `integrations/`, but the explicit archive-consumer test does
not currently cover LangChain or Pydantic AI. Directory copying alone is not
release evidence.

## Scope

In scope:

- Make Issue #194 the only active execution tracker while preserving Issue #3
  as a closed historical epic.
- Define one strict, machine-readable inventory for the twelve current
  integration roots.
- Make Standard and Full release archives contain the exact reviewed
  integration inventory and no workspace-only state.
- Exercise command-host integrations from an extracted archive.
- Install and smoke-test Python/framework integrations from an extracted
  archive and their declared lock state.
- Reconcile archive metadata, internal and external checksums, SPDX evidence,
  dependency-license evidence, and release workflows for the retained
  integration surface.
- Record an explicit Go release-version decision from the exact previous-tag
  diff. Do not infer a version from a PR title or the presence of one CLI
  subcommand.
- Publish and verify a new release only if the tracker/version ruling records
  publication as required for Phase 1 closure.
- Update only Phase 1 checkboxes after exact-Head, merge, post-main, and any
  required published-release evidence is read back.

Out of scope:

- All seekDB and OceanBase code, tests, workflows, assets, licenses, SBOM
  entries, documentation, capability claims, and release-parity evidence.
- Every Issue #194 Phase 5, Phase 6, and Phase 7 checkbox.
- Closing Issue #194.
- Creating, forking, or maintaining an external consumer to satisfy Issue
  #198.
- Changing the OpenAPI contract, generated API, public Go API, persistence
  formats, database behavior, or runtime search capabilities.
- Persisting or printing credentials, prompts, source or Memory content, raw
  scope IDs, database locations, or private workspace paths.

## Authoritative Integration Inventory

The reviewed inventory contains exactly twelve roots:

| ID | Class | Required release entrypoint |
| --- | --- | --- |
| `bub` | Python package | `integrations/bub/pyproject.toml` |
| `claude-code` | Command host | `integrations/claude-code/plugins/powercontext/.claude-plugin/plugin.json` |
| `codex` | Command host | `integrations/codex/plugins/powercontext/.codex-plugin/plugin.json` |
| `dsh` | Command host | `integrations/dsh/plugins/powercontext/lib/index.js` |
| `hermes` | Command host | `integrations/hermes/plugins/powercontext/plugin.yaml` |
| `langchain` | Python package | `integrations/langchain/pyproject.toml` |
| `langgraph` | Python package | `integrations/langgraph/pyproject.toml` |
| `openclaw` | Command host | `integrations/openclaw/plugins/memory-powercontext/dist/index.js` |
| `opencode` | Command host | `integrations/opencode/plugins/powercontext/lib/index.js` |
| `pi` | Command host | `integrations/pi/plugins/powercontext/extensions/powercontext.ts` |
| `pydantic-ai` | Python package | `integrations/pydantic-ai/pyproject.toml` |
| `workbuddy` | Command host | `integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json` |

The inventory is explicit rather than directory-derived because it must also
record the consumer mode and reviewed entrypoints. Validation is bidirectional:
an unclassified repository root and a stale inventory entry both fail.

## Architecture

`build/release-integrations.json` is a versioned release input. A small parser
inside `tools/release` rejects duplicate and unknown fields, unsafe paths,
unknown consumer classes, missing roots, missing entrypoints, and repository
roots absent from the inventory.

The packager consumes this inventory when staging integrations. The archive
tests are split by observable responsibility:

1. staging and exact file inventory;
2. command-host setup from an extracted archive;
3. Python package installation and public smoke from an extracted archive;
4. checksum, SPDX, license, and workflow evidence.

Each responsibility owns separate files so independent workers can implement
and review them without editing the same source. A small inventory foundation
lands first; the four implementation lanes then run in parallel.

## Release and SBOM Boundary

Retained integration source, manifests, lock files, and tracked executable
bundles are redistributed content. The release evidence must name every
redistributed integration and preserve its license metadata. Lock-file
transitive dependencies that are not installed into the archive must not be
misrepresented as bundled native/runtime dependencies. The verifier must still
prove that the archived package can install from its lock state.

Standard and Full archives expose the same integration inventory. Edition
differences remain limited to their existing native inference assets.

## Parallel Delivery Contract

After the inventory foundation lands, work proceeds in isolated worktrees:

- staging lane owns `package.go`, `files.go`, and staging tests;
- command-consumer lane owns the existing archive-consumer test or a dedicated
  replacement file;
- Python-consumer lane owns a new Python integration consumer test;
- evidence lane owns evidence/checksum/license code and release workflows;
- documentation/version lane owns release documentation and the version
  decision.

No worker may edit another lane's files. A discovered cross-lane requirement
is reported to the coordinator and incorporated through an ordered follow-up,
not by silently widening ownership.

## Acceptance

Phase 1 is ready for closure only when:

- the exact twelve-root inventory is validated against current `main`;
- Standard and Full extracted archives contain the reviewed entrypoints and no
  workspace-only state;
- all eight command-host integrations consume the extracted archive;
- Bub, LangChain, LangGraph, and Pydantic AI install and smoke-test from the
  extracted archive and declared lock state;
- WorkBuddy registration invokes only the extracted archive binary;
- archive checksums, SPDX evidence, license evidence, and workflow contracts
  agree;
- focused tests, exact PR Head checks, merge state, and post-main checks are
  read back;
- any required published release is verified from downloaded assets;
- only the four Phase 1 leaf boxes and Phase 1 tracking box are checked.

Issue #194 remains open with backend phases untouched.
