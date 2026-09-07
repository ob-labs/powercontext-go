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

`canonical/sources-manifest.json` independently projects `create_source` and
`get_source` from that same pinned upstream blob into `canonical/sources.json`
and `api/canonical/sources`. Server composition dispatches only the exact generated
method/path matches through the existing authentication, request-body, tracing,
and error middleware. Source resource creation allocates a server-owned identity;
the legacy capture API retains its caller-owned identity and text semantics.

Source resource content preserves JSON types, including explicit null and empty
strings. Digests use RFC 8785 without Unicode normalization. The stored wire value
also preserves integer versus floating-point tokens: a valid float such as
`9007199254740992.0` must not become an out-of-domain integer after restart.
Unknown Scope admission remains a redacted HTTP 404. The pinned `create_source`
schema does not declare that status, so its generated client reports an unexpected
status for this case; this is an explicit upstream typed-client coverage gap.

`mcp_generated_openapi_operations` enumerates only the fixed curated legacy
OpenAPI-dispatch tools. Native conditional tools such as the Handoff picker and
Scope Binding tools are registered and tested separately. A staged Scope
operation or a schema inventory entry alone does not expose a generated MCP
tool.

`canonical/artifacts-manifest.json` projects only `get_artifact` and
`get_artifact_revision` from the same pinned upstream blob. The independent
`api/canonical/artifacts` package is mounted through the existing HTTP policy
chain and the Runtime-owned scoped reader. SQLite resolves the head or exact
revision, its ordered lineage, and any Skill package in one read transaction.
Artifact content is projected from decoded domain values; package-backed Skills
rehydrate and validate the immutable package before returning its public content
and package reference. The storage-only `package_ref` never appears on the wire.
Artifact resource digests use RFC 8785 without extra Unicode normalization, as
the pinned upstream `builtin/persistence/records.py` does. NFC and NFD domain
content remain distinct. Existing domain validation, including Memory reason
normalization during decoding, is preserved.

The [Issue #202 lineage policy](https://github.com/ob-labs/powercontext-go/issues/202#issuecomment-5575546837)
overlays only this sidecar's `ArtifactRevision` source lineage to preserve
`content`, `external-skill-snapshot`, and `accepted-observation` in their stored
order. Scope and Source schemas remain unchanged. Head GET uses the pinned
upstream strong validator `"revision:N"`; exact `If-None-Match` equality returns
304 with ETag and request ID and no body. Exact revision GET does not substitute
the head or use its cache validator. Artifact list, create, and replace remain
deferred, and these two GETs add no MCP tools.

OpenAPI 3.0 cannot encode the combined `source_refs` + `artifact_refs` maximum
described by the Candidate schemas. The generator derives the affected model
set from the two `maxItems: 32` declarations and emits the supplemental
`api/v1.ValidatePowerContextContract` validator. HTTP, MCP, and the public Go
Client all invoke it at their decoded transport boundary.
