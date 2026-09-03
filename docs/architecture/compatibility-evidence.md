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
| CLI output and process behavior | `cmd/powercontext` public command boundary | `tools/process-smoke` exercises the built binary, including authentication, restart persistence, and shutdown. |
| Public Go APIs | Deliberate public package inventory in ADR 0002 | `make api-compat` binds `v0.1.0` to its exact commit and rejects incompatible exported API changes. |

## Quality scope

The Go lint gate excludes generated code only at `api/v1/`,
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
