# Upstream Master Phase 0 Design

## Goal

Establish a reproducible, fail-closed evidence boundary for aligning
PowerContext Go with `oceanbase/powercontext` `master` while accepting only
SQLite, Codex, and WorkBuddy as the supported product matrix.

## Fixed Inputs

- Upstream snapshot: `oceanbase/powercontext@74b961fbb07165595314726715d412a3d0d90589`.
- Go baseline: `ob-labs/powercontext-go@e9dce330e58c4acbcfb1c0c5f0a4c9df581750e9`.
- Supported database: `sqlite`.
- Supported external agents: `codex`, `workbuddy`.

The frozen v0.0.2 Oracle and v0.1.0 release target remain historical
evidence. Phase 0 must not rewrite either target, claim latest-master parity,
or change product runtime behavior.

## Design

An external detached upstream worktree and lockfile-driven Python environment
produce raw collection evidence outside both repositories. Version-controlled
Go evidence contains only normalized, path-safe data derived from that
collection.

`parity-scope.json` is a strict product-matrix contract. A latest-master case
must be explicitly `in_scope`, `out_of_scope`, or `mixed`. `mixed` records a
node that asserts an in-scope core, SQLite, Codex, or WorkBuddy behavior while
also using a non-target adapter or backend as a comparison or isolation input.
It neither grants support to the non-target object nor allows its assertions to
count as mapped. Exclusions and mixed entries name their bounded reason and
factual rationale, so a test that mixes host targets cannot silently erase a
supported product claim. Existing release parity generation continues to work
before the latest inventory exists.

`upstream-master-openapi-ledger.json` records the complete API boundary rather
than treating `77 - 53` as a list of additions. It preserves Go-only Handoff
Report extensions and treats the `get_stats` `GET` to `POST` change as a method
migration. Its validator compares the committed ledger with two explicit YAML
inputs, so a count alone cannot pass.

## Acceptance Criteria

- The external collector proves the exact upstream SHA, lockfile identity,
  interpreter identity, collection exit status, normalized node IDs, and checksums.
- The scope contract rejects unknown fields, duplicate entries, unsupported
  database/agent declarations, unknown discovery cases, stale scope entries,
  and exclusions with missing reasons.
- Mixed cases must state one or more supported surfaces and every non-target
  database or external agent used as a comparator; a mixed node cannot count
  as evidence for its unsupported portion.
- The API ledger rejects missing, duplicate, unknown, or misclassified operations;
  it verifies the 38/14 operation-ID and 39/15 endpoint comparisons directly.
- CI runs the new evidence checks without replacing frozen-release evidence or
  making any unsupported implementation a required successful product path.

## Non-Goals

- Runtime typed-unsupported behavior for non-SQLite databases and non-Codex/
  WorkBuddy agents. That belongs to the next product-boundary phase.
- Source, Scope, Artifact, managed-Skill, remote-Skill, or service lifecycle
  behavior implementation.
- seekDB, OceanBase, or any third external-agent migration, test, archive, or
  documentation support.
