# Remote Skill Target Lifecycle Design

## Decision

Implement the first usable remote managed-Skill lifecycle as an independently
generated canonical sidecar. It supports only remote target `create`, `list`,
`enroll`, `rename`, and `revoke` operations. It owns durable SQLite target
identity, one-time enrollment, credential revocation, and generation CAS.

Publication desired state, receiver reconciliation, package download, and
delivery receipts are a later slice. They require an exact package-backed
Skill revision, publication generation state, and target-bearer receiver
authorization. They must not be represented as implemented before those
contracts exist.

## Product Boundaries

- SQLite is the only product database. The target registry is SQLite schema
  owned by `internal/sqlstore`; it does not add seekDB, OceanBase, or a shared
  MySQL schema path.
- Remote target `agent_kind` has exactly two values: `codex` and `workbuddy`.
  The upstream raw schema's `claude_code` value is excluded by a generated
  projection overlay, the domain constructor, and SQLite `CHECK` constraints.
- The frozen legacy `openapi/powercontext.yaml`, `api/v1`, legacy Client
  Invoker, and legacy 53 operation surface remain unchanged.
- Remote target routes are a new canonical sidecar. They are not MCP tools.
- A remote target has no server-interpreted local path, local publish flag, or
  external-host filesystem behavior. Existing `AgentSkillTarget` remains the
  separate local projection configuration type.

## Target State

One durable target is identified by `(scope_id, target_id)` and has one of:

| State | Required durable values | Forbidden durable values |
| --- | --- | --- |
| `pending` | enrollment code digest and expiry | installation identity and credential verifier |
| `active` | installation identity, credential subject, credential verifier, receiver metadata | enrollment code digest and expiry |
| `revoked` | identity and historical non-secret metadata | enrollment code digest and credential verifier |

The target retains a non-negative `generation`. `rename` and `revoke` require
an exact expected generation. A successful mutation increments generation;
an idempotent rename to the same display name and revoke of an already revoked
target return the current row without mutation. Revocation is a tombstone, not
a delete, so later receipts and publication state retain an auditable target
identity.

The database stores only SHA-256 digests of the enrollment code and target
credential. The raw enrollment code is returned once by `create`; the raw
target credential is returned once by `enroll`. Neither can appear in status,
logs, traces, errors, or a persisted payload.

## Runtime And SQLite

`artifact/skill` owns immutable target values, validation, and typed public
errors. `internal/sqlstore` owns `pc_agent_skill_targets`, schema creation,
short transactions, and compare-and-swap updates. It does not import Runtime
or HTTP packages.

`internal/runtime` owns Scope admission and target lifecycle orchestration:

- `List` uses `Runtime.ScopedRead`.
- `Create`, `Rename`, and `Revoke` use `Runtime.ScopedWrite`; ID and secret
  creation occur only after Scope admission succeeds.
- `Enroll` first resolves a digest to a pending target in one SQLite
  transaction, then consumes it with generation CAS in that target's scoped
  write lane. Invalid, expired, already consumed, or revoked codes map to one
  redacted enrollment refusal without revealing target existence.

SQLite enforces both the two-agent enum and the state-payload shape with
`CHECK` constraints. Initial creation is a direct unique-key insert; updates
use `WHERE scope_id = ? AND target_id = ? AND generation = ?`, then re-read a
zero-row update to distinguish missing target from stale generation.

## Canonical HTTP Contract

A new `api/canonical/remoteskills` package is generated from
`openapi/canonical/remote-skills.json`. Its source manifest selects exactly:

```text
list_remote_skill_targets
create_remote_skill_target
enroll_remote_skill_target
rename_remote_skill_target
revoke_remote_skill_target
```

The raw upstream source stays pinned with existing sidecars. The canonical
projection overlays `RemoteAgentKind` to `["codex", "workbuddy"]`; this is
the wire-level support boundary and is validated by generator mutants.

`list`, `create`, `rename`, and `revoke` retain the process-wide static Server
bearing-auth boundary. `enroll` is the only static-bearer exemption, matched by
its exact generated `POST` path, never a path prefix. It still passes through
the shared request size, Unicode, request ID, trace, metrics, redacted-error,
and access-log middleware.

Enrollment accepts TLS requests and direct loopback HTTP only. It rejects
non-loopback plaintext before code lookup. This first slice does not trust
forwarded-protocol headers or add an insecure remote HTTP opt-in.

## Verification

Completion of this slice requires all of the following:

1. Domain tests prove the Codex/WorkBuddy enum, pending/active/revoked shape,
   validation, secret-free status projection, and generation semantics.
2. SQLite tests prove schema constraints, restart persistence, no raw secret
   persistence, same-name view refusal, insert/update conflicts, and two
   independent Database/Runtime instances cannot consume one enrollment code
   or win the same generation CAS.
3. Runtime tests prove unknown Scope causes no store call or secret issuance,
   valid lifecycle transitions, and redacted enrollment failure.
4. Generator tests prove the selected five operations, raw-source pin,
   compatibility ledger status, and `RemoteAgentKind` overlay. Fresh generated
   consumers must tidy, verify, and test.
5. Real HTTP generated-client tests prove the five routes, exact bearer
   exemption, TLS/loopback transport refusal, request/body policy, auth,
   conflict/not-found/validation behavior, and access-log/trace redaction.
6. `make check-generated`, focused SQLite and Runtime checks, race/vet/lint,
   exact PR Head CI, and post-main CI pass before Issue evidence is updated.

## Deferred Consequences

This slice does not publish a Skill, materialize it on a receiver, or provide
target-bearer package access. The remaining remote operations stay `deferred`
in the compatibility ledger until publication desired state, reconciliation,
exact package download, receipt semantics, and Codex/WorkBuddy archive
consumer evidence are implemented together.
