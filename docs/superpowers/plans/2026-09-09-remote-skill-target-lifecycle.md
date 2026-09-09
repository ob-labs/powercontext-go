# Remote Skill Target Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the SQLite-only, Codex/WorkBuddy-only remote target lifecycle through a separate canonical HTTP sidecar.

**Architecture:** A Skill domain model and SQLite repository hold remote target enrollment state. A Runtime application owns Scope admission, secret issuance, and generation CAS. A separately generated canonical sidecar exposes five lifecycle operations while preserving frozen legacy and existing managed-Skill sidecars.

**Tech Stack:** Go 1.27, SQLite, existing Runtime scoped locks, Ogen canonical sidecars, SHA-256 digests, Go HTTP TLS/loopback policy.

**Spec:** `docs/superpowers/specs/2026-09-09-remote-skill-target-lifecycle-design.md`

## Global Constraints

- SQLite only; do not add or advertise seekDB or OceanBase.
- Target agent kinds are exactly `codex` and `workbuddy`; reject all others before durable side effects.
- Preserve frozen `openapi/powercontext.yaml`, `api/v1`, legacy Client Invoker, legacy 53 operations, and MCP inventory.
- Add a separate canonical remote-Skill sidecar; do not extend existing managed-Skill sidecar or MCP tools.
- Persist SHA-256 secret digests only. Do not log, trace, persist, or re-render raw enrollment codes or credentials.
- `enroll` is the only exact static-bearer exemption. Require TLS or direct loopback HTTP.
- Every new behavior starts with a failing test and follows Modern Go 1.27 guidance.

---

### Task 1: Immutable Remote Target Domain

**Files:**
- Create: `artifact/skill/remote_target.go`
- Create: `artifact/skill/remote_target_test.go`

**Produces:** `RemoteTargetState`, immutable `RemoteTarget`, credential-free `RemoteTargetView`, and typed redacted target errors.

- [ ] Write failing tests for `pending`, `active`, and `revoked` target shapes, the exact Codex/WorkBuddy enum, generation validation, and views that omit every digest.
- [ ] Run `go test -count=1 ./artifact/skill -run '^TestRemoteTarget'` and confirm missing target APIs cause the failure.
- [ ] Implement constructors/accessors that validate target ID, display name, `project`/`agent_pull`, timestamps, state-specific secret fields, and `validAgentKind`.
- [ ] Re-run the focused test until it passes, then commit `feat(skill): model remote target lifecycle`.

### Task 2: SQLite Target Repository And Schema

**Files:**
- Create: `internal/sqlstore/skill_distribution_schema.go`
- Create: `internal/sqlstore/remote_skill_targets.go`
- Create: `internal/sqlstore/remote_skill_targets_test.go`
- Modify: `internal/sqlstore/schema.go`

**Consumes:** Task 1 values.

**Produces:** SQLite-only `Create`, `Get`, `List`, `FindByEnrollmentDigest`, and `Replace(expectedGeneration)`.

- [ ] Write failing SQLite tests for table creation, same-name-view refusal, restart persistence, no raw secret bytes, target-kind `CHECK`, duplicate insert, stale generation, and missing-target distinction.
- [ ] Run `go test -count=1 ./internal/sqlstore -run '^(TestRemoteSkillTarget|TestSQLiteSkillDistribution)'` and confirm failure from missing schema/repository behavior.
- [ ] Implement `pc_agent_skill_targets` with `(scope_id,target_id)` primary key, scope FK, unique digest identities, the two-agent `CHECK`, exact state-payload `CHECK`, and non-negative generation. Add direct-insert create and `UPDATE ... WHERE generation = ?` CAS with zero-row re-read. Map an absent row to not found and an existing generation mismatch to conflict.
- [ ] Re-run the focused tests and commit `feat(sqlite): persist remote skill targets`.

### Task 3: Scoped Runtime Lifecycle

**Files:**
- Create: `internal/runtime/remote_skill_target_application.go`
- Create: `internal/runtime/remote_skill_target_application_test.go`
- Create: `internal/sqlstore/runtime_remote_skill_targets.go`
- Create: `internal/sqlstore/runtime_remote_skill_targets_test.go`
- Modify: `server/application_services.go`
- Modify: `server/application.go`

**Consumes:** Tasks 1-2.

**Produces:** `Create`, `List`, `Enroll`, `Rename`, and `Revoke` lifecycle operations.

- [ ] Write failing Runtime tests proving unknown Scope makes no store call and invokes neither ID nor secret generator; enrollment consumes a code once; rename/revoke require generation; invalid enrollment does not reveal target identity.
- [ ] Run `go test -count=1 ./internal/runtime ./internal/sqlstore -run 'RemoteSkillTarget'` and confirm missing lifecycle APIs cause failure.
- [ ] Implement `ScopedRead` list, `ScopedWrite` create/rename/revoke, injected clock/ID/secret dependencies, and atomic pending-to-active enrollment CAS with one short SQLite transaction per store method.
- [ ] Run focused normal and race tests, then commit `feat(runtime): enroll remote skill targets`.

### Task 4: Canonical Remote Target Generator

**Files:**
- Create: `openapi/canonical/remote-skills-manifest.json`
- Create: `openapi/canonical/remote-skills.json`
- Create: `tools/api-generate/canonical_remote_skills.go`
- Create: `tools/api-generate/canonical_remote_skills_test.go`
- Create: `api/canonical/remoteskills/` generated output
- Modify: `tools/api-generate/main.go`, `openapi/compatibility-surface.json`, `.golangci.yml`, `.licenserc.yaml`, `Makefile`, `test/generator-inventory.json`, `tools/release/workflow_test.go`

**Consumes:** Task 3 lifecycle and existing raw upstream pin.

**Produces:** generated `remoteskills` client/server types with `codex | workbuddy` RemoteAgentKind overlay.

- [ ] Write failing generator tests for exact selected IDs/methods/paths, pinned raw digest, compatibility ledger status, a `claude_code` overlay mutant, and a fresh generated consumer.
- [ ] Run `go test -count=1 ./tools/api-generate -run 'RemoteSkill'` and confirm generator absence is the failure.
- [ ] Implement a separate generator mode/root, project five lifecycle operations only, overlay RemoteAgentKind, change only selected ledger statuses to `implemented-canonical`, and add each generated root to explicit lint/license/Make/release inventories.
- [ ] Run focused generator tests, full generator tests, and `make check-generated`; commit `feat(api): generate remote skill target sidecar`.

### Task 5: Exact HTTP Auth And Public Lifecycle

**Files:**
- Create: `internal/endpoint/canonical_remote_skill.go`
- Create: `internal/endpoint/canonical_remote_skill_test.go`
- Create: `server/canonical_remote_skill.go`
- Create: `server/canonical_remote_skill_http_test.go`
- Modify: `server/http.go`, `internal/httpapi/middleware.go`, `internal/httpapi/middleware_test.go`, `server/application.go`

**Consumes:** Tasks 3-4.

**Produces:** generated `FindPath` dispatch with static bearer on administration and an exact safe-transport enroll exemption.

- [ ] Write failing real-handler/generated-client tests for all five operations, static bearer requirements, exact non-enroll rejection, one-time enrollment, TLS/direct-loopback success, non-loopback plaintext refusal before lookup, no MCP registration, and access-log/trace redaction.
- [ ] Run `go test -count=1 ./server ./internal/httpapi ./internal/endpoint -run 'RemoteSkill'` and confirm missing sidecar dispatch/auth exemption causes failure.
- [ ] Implement wire mapping, exact generated-path dispatch, and an exact `POST enroll` middleware exemption. Preserve all shared outer policy and reject plaintext remote enrollment before Runtime lookup.
- [ ] Run focused normal/race/vet checks; commit `feat(server): operate remote skill target lifecycle`.

### Task 6: Review And Merge Evidence

**Files:**
- Modify: shipped-contract documentation only if it accurately describes the completed lifecycle and deferred publication boundary.
- Modify: `AGENTS.md` only if a reusable rule is proven by a repaired regression.

- [ ] Review changed tests with Test Guard: real SQLite for storage, real generated client/handler for transport, no internal-mock assertions.
- [ ] Run focused SQLite/Runtime/HTTP/race/vet/lint, `make check-generated`, `make SHELL=D:/programs/cmder/vendor/git-for-windows/bin/bash.exe api-compat`, and relevant release consumer checks.
- [ ] Obtain task and whole-branch review, create a PR on current main, require exact-Head CI, merge via repository-supported strategy, verify post-main CI, and add an evidence-only #202 comment that keeps publication/reconcile/download/receipt deferred.
