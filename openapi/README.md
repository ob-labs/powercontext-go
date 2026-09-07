# OpenAPI

`powercontext.yaml` is copied from the current Python reference baseline and is
the sole source of truth for the generated legacy HTTP wire surface.

`compatibility-surface.json` pins the later upstream operation inventory while
the corresponding use cases are implemented incrementally. Its 53-operation
legacy baseline includes the 14 retained Go Handoff Report extensions and
legacy `GET /v1/stats`. Every one of the 38 pinned upstream-only operations is
explicitly `deferred` or `implemented-canonical`. Both states are excluded
from the frozen legacy input, so a status change alone cannot expose a legacy
route, generated package, or MCP tool. An `implemented-canonical` status
requires its separately generated canonical transport plus Server and SQLite
service-chain proof before it is recorded.

The canonical ledger contains 77 operations. It removes the
retained Handoff extensions, normalizes `get_stats` to its reserved canonical
`POST /v1/stats` `GetStatsCanonical` name, and adds all 38 upstream-only
operations regardless of their staged status. The inventory remains a
migration gate, not a second OpenAPI input, so it cannot expand `v1.Handler`
or `v1.Invoker` on its own.

`canonical/upstream-powercontext.yaml` is the immutable raw OpenAPI blob from
`oceanbase/powercontext@74b961fbb07165595314726715d412a3d0d90589`.
`canonical/scopes-manifest.json` pins its SHA-256, verifies every one of the
77 ledger endpoints, and explicitly projects only `list_scopes`, `get_scope`,
`get_default_scope`, `resolve_scope_selection`, and `resolve_scope_binding`.
The projection writes `canonical/scopes.json` and generates the separate
`api/canonical/scopes` package. Its only product overlay narrows
`ScopeBindingKey.integration` to `codex` and `workbuddy`.

The sidecar generator cannot target `api/v1`, cannot write the legacy Client
Invoker, and does not register HTTP routes or MCP tools. Its generated package
is a compile-time contract for a later Server and SQLite Scope-read slice, not
evidence that those endpoints are mounted.

`mcp_generated_openapi_operations` enumerates only the fixed curated legacy
OpenAPI-dispatch tools. Native conditional tools such as the Handoff picker and
Scope Binding tools are registered and tested separately. A staged Scope
operation or a schema inventory entry alone does not expose a generated MCP
tool.

OpenAPI 3.0 cannot encode the combined `source_refs` + `artifact_refs` maximum
described by the Candidate schemas. The generator derives the affected model
set from the two `maxItems: 32` declarations and emits the supplemental
`api/v1.ValidatePowerContextContract` validator. HTTP, MCP, and the public Go
Client all invoke it at their decoded transport boundary.
