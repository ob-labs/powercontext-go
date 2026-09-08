# Compatibility evidence matrix

PowerContext Go treats compatibility as independent observable surfaces. A
passing check on one surface does not prove another.

| Surface | Source of truth | Required evidence |
| --- | --- | --- |
| HTTP wire contract | `openapi/powercontext.yaml` | `make check-generated` and `make contract-test` regenerate and exercise the OpenAPI transport. |
| Persisted data | Immutable Artifact and Memory authority state plus versioned SQLite fixtures | Frozen Oracle, SQLite, and process-restart checks preserve authority and projection behavior separately. |
| Scheduler state | Versioned scheduler fixture and constrained Pickle model | Frozen Oracle fixture regeneration and `internal/scheduler` compatibility tests. |
| Prompts | Embedded prompt files and frozen SHA-256 fixture inventory | Frozen Oracle and traceability checks reject prompt or digest drift. |
| Generated schemas and clients | Generator source and checked-in generated inventory | `make check-generated`; fresh-consumer execution is owned by the generated-consumer gate. |
| Fixed upstream74 Source sidecar | `openapi/canonical/sources.json` and Runtime Scope admission | Real Server requests verify Definition, Observation, and checkpoint behavior. The fixed raw schema omits `404 scope_not_found` for both `get_connector_checkpoint` and `commit_connector_checkpoint`; Runtime admission still returns that redacted 404 before storage, so generated clients report an unexpected status for those two cases. Do not add a synthetic generated response to hide the contract gap. |
| Fixed upstream74 Stats sidecar | `openapi/canonical/stats.json` and Runtime Scope selection | Real Server requests prove canonical `POST /v1/stats` resolves the complete selection before any SQLite Scope reader runs, returns additive aggregate and per-Scope snapshots, and leaves legacy `GET /v1/stats` unchanged. The pinned Stats schema omits `404 scope_not_found`; Runtime selection still returns that redacted 404 before any selected reader runs, so generated clients report an unexpected status. Do not turn that admission failure into `422` or add a synthetic response. |
| CLI output and process behavior | `cmd/powercontext` public command boundary | `tools/process-smoke` exercises the built binary, including authentication, restart persistence, and shutdown. |
| Public Go APIs | Deliberate public package inventory in ADR 0002 | `make api-compat` binds `v0.1.0` to its exact commit and rejects incompatible exported API changes. |

## Canonical Source Sidecar Evidence

The Source sidecar mounts only the exact generated HTTP method/path pairs. Real
Server composition tests prove that the legacy `capture_content_source` HTTP
operation remains reachable through the underlying legacy handler, while MCP
`tools/list` continues to expose that legacy tool and excludes the four
upstream-only Source Definition, Observation, and Connector checkpoint
operations. This transport boundary is deliberate; no MCP tool or generated
raw74 response is added merely to make a typed client accept a Runtime Scope
admission result.

## Quality scope

The Go lint gate excludes generated code only at the explicit owned roots
`api/v1/`, `api/canonical/scopes/`, `api/canonical/sources/`,
`api/canonical/artifacts/`, `api/canonical/stats/`,
`client/invoker_gen.go`, and `internal/mcpapi/schemas_gen.go`. The generated
files still build through contract and module-integrity gates. Coverage does
not exclude generated packages or low-coverage command surfaces: it runs
race-enabled atomic coverage over `./...` and rejects skipped-only success.

The retained sqlite-vec amalgamation is a byte-retained native dependency. Its
two exact C and header paths are excluded from the Apache header fixer, while
the Go wrapper remains checked. The retained JavaScript and TypeScript build
outputs under the DSH, OpenCode, and OpenClaw integration paths keep their
license headers and are verified by their host-adapter build and generated-diff
steps; they are not broadly excluded from repository quality evidence.

## Version boundaries

`v0.1.0` currently publishes the root Go module and its release binary. The
checked-in `test/downstream` module is an external-consumer fixture, not an
independently released tool module and must not receive a release tag or
changelog version. Tools under `tools/` belong to the root module unless a
future release explicitly gives one a separate module, tag namespace,
changelog, and release-verification contract.

No exported identifiers have been removed between `v0.1.0` and the initial
post-release baseline. A type alias or Deprecated forwarding surface is added
only when an actual incompatible comparison identifies a cheaper safe bridge;
it is not preemptively added without a removed import or identifier.
