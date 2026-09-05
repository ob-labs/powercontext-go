# Supported Product Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce SQLite, Codex, and WorkBuddy as the only supported product matrix before later upstream-master feature ports.

**Architecture:** Validate unsupported databases and agent targets at the earliest public boundary, preserve typed causes through CLI usage errors, and reduce release/CI evidence to the same supported matrix. Historical source remains tracked but cannot execute or ship as supported behavior.

**Tech Stack:** Go 1.27, Cobra, SQLite/sqlite-vec, JSON release manifests, GitHub Actions, Go tests, Python host smoke tests.

**Spec:** `docs/superpowers/specs/2026-09-06-supported-product-boundary-design.md`

## Global Constraints

- Supported databases are exactly `sqlite`.
- Supported external agents are exactly `codex` and `workbuddy`.
- Unsupported errors are typed, redacted, and raised before network, database, subprocess, or filesystem side effects.
- Historical v0.1.0 Oracle evidence and Phase 0 upstream-master evidence remain unchanged.
- Do not delete retained adapter source trees in this phase.

---

### Task 1: Enforce database and Agent Skill target boundaries

**Files:**
- Modify: `server/config_validate.go`
- Modify: `server/config_test.go`
- Modify: `artifact/skill/external.go`
- Modify: `artifact/skill/external_targets.go`
- Modify: `artifact/skill/projection.go`
- Modify: focused tests under `artifact/skill`

- [x] Add failing tests that require a typed, redacted unsupported database error for seekDB and OceanBase before profile validation.
- [x] Add failing tests that accept Codex and WorkBuddy Agent Skill targets and reject Claude Code/unknown targets before path resolution.
- [x] Implement the smallest typed errors and WorkBuddy projection label/format support.
- [x] Remove or update obsolete tests that assert current OceanBase/Claude support only after the new regressions pass.
- [x] Run focused Server and Agent Skill tests plus `git diff --check`.

### Task 2: Enforce CLI setup and doctor boundaries

**Files:**
- Modify: `internal/cli/setup.go`
- Modify: `internal/cli/doctor.go`
- Modify: `internal/cli/integration_catalog.go`
- Modify: `internal/cli/setup_select.go`
- Modify: adjacent CLI tests

- [x] Add failing public-command tests for non-target setup, doctor, and selection inputs; assert exit code 2, typed cause matching, and zero executor/filesystem mutations.
- [x] Register real Codex/WorkBuddy commands and typed unsupported stubs for historical command names.
- [x] Limit supported selection and aggregate diagnostics to Codex and WorkBuddy.
- [x] Run focused CLI tests without relying on Windows symlink privilege.

### Task 3: Align release inventory, documentation, and CI

**Files:**
- Modify: `build/release-integrations.json`
- Modify: `build/release-integration-files.txt`
- Modify: `tools/release/integration_inventory.go`
- Modify: focused release inventory/archive tests
- Modify: `README.md`, `docs/release/INSTALL.md`, `.github/workflows/README.md`
- Modify: relevant workflow contract files

- [x] Add failing archive tests requiring exactly Codex and WorkBuddy integration roots in Standard and Full artifacts.
- [x] Separate tracked source-root discovery from supported release inventory validation.
- [x] Regenerate the reviewed file inventory and update release evidence contracts.
- [x] Update documentation and workflow summaries to the exact SQLite/Codex/WorkBuddy matrix.
- [x] Run release inventory/archive tests, docs build, actionlint, and generated checks.

### Task 4: Verify the supported service chains

**Files:**
- Modify only if a regression test exposes a real gap: `test/e2e`, Codex integration tests, or WorkBuddy service-chain tests.

- [ ] Run SQLite public and bearer-authenticated Codex service-chain evidence.
- [ ] Run SQLite public and bearer-authenticated WorkBuddy Hook/MCP evidence.
- [ ] Run focused Server/CLI/Skill/release tests together and inspect output.
- [ ] Record exact remaining Windows environment limitations separately from behavior failures.
