# OpenAPI

`powercontext.yaml` is copied from the current Python reference baseline and is
the sole source of truth for the generated legacy HTTP wire surface.

`compatibility-surface.json` pins the current-master operation inventory while
the corresponding use cases are implemented incrementally. Its 53-operation
legacy baseline includes the 14 retained Go Handoff Report extensions and
legacy `GET /v1/stats`. The current-master inventory is pinned to
`oceanbase/powercontext@e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c`: 94 canonical
operations, 55 upstream-only operations, and 17 newly deferred operations.
Every upstream-only operation is explicitly `deferred` or
`implemented-canonical`. Both states are excluded from the frozen legacy
input, so a status change alone cannot expose a legacy route, generated
package, or MCP tool. An `implemented-canonical` status requires its separately
generated canonical transport plus Server and SQLite service-chain proof before
it is recorded.

The current-master canonical ledger removes the retained Handoff extensions,
normalizes `get_stats` to its reserved canonical `POST /v1/stats`
`GetStatsCanonical` name, and includes all upstream-only operations regardless
of their staged status. It is a migration gate, not a second OpenAPI input, so
it cannot expand `v1.Handler` or `v1.Invoker` on its own.

Scope, Source, Artifact, and Stats sidecars remain independently pinned to
`oceanbase/powercontext@74b961fbb07165595314726715d412a3d0d90589`.
The Scope, Source, Artifact, and managed-Skill manifests pin the raw-source
SHA-256 and validate only each selected operation's ID, method, path, and
`implemented-canonical` status against the e4 inventory. The Stats manifest
instead validates the reserved `get_stats` method migration from legacy GET to
canonical POST. None require the stable 74 schema source to contain every new
e4 deferred operation. The e4 Artifact revision-history, Artifact tag, Prompt,
and Access clusters remain deferred. This rebaseline does not implement Artifact
writes, managed Skill generation or lifecycle, remote Skills, or native personal services.

`canonical/upstream-powercontext.yaml` is the immutable raw OpenAPI blob for
the four sidecars. `canonical/scopes-manifest.json` explicitly projects only
`list_scopes`, `get_scope`, `get_default_scope`, `resolve_scope_selection`, and `resolve_scope_binding`.
The projection writes `canonical/scopes.json` and generates the separate
`api/canonical/scopes` package. Its only product overlay narrows
`ScopeBindingKey.integration` to `codex` and `workbuddy`.

The sidecar generator cannot target `api/v1`, cannot write the legacy Client
Invoker, and does not register HTTP routes or MCP tools. Its generated package
is a compile-time contract for a later Server and SQLite Scope-read slice, not
evidence that those endpoints are mounted.

The frozen legacy `openapi/powercontext.yaml`, generated `api/v1`, legacy
Client Invoker, and generated MCP tool inventory remain unchanged by the e4
inventory update. The two pins deliberately separate current-master deferred
accountability from the stable public sidecar wire contract.

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

`canonical/artifacts-manifest.json` projects only `list_artifacts`,
`get_artifact`, and `get_artifact_revision` from the same pinned upstream blob. The independent
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

`canonical/managed-skills-manifest.json` projects two exact package reads and
one bounded immutable usage-evidence write. The manifest and download reads are
verified only for a Go-persisted immutable package snapshot: `archive_base64`
decodes to the exact stored archive bytes, and its manifest and package reference
describe that same verified snapshot. `record_skill_usage` first verifies the
exact package-backed Skill Revision and tree digest, then records only bounded
Source journal evidence; it does not materialize, publish, or replace an
Artifact, and it never becomes an MCP tool. These routes do not define a shared
cross-language ZIP canonicalization or establish an archive/reference produced
by another implementation as equivalent. Python Receiver/CLI archive-reference
interoperability remains unimplemented because their full package-reference
calculation uses different ZIP byte canonicalization. Archive writer
canonicalization and immutable-record migration remain separate future work.

The [Issue #202 lineage policy](https://github.com/ob-labs/powercontext-go/issues/202#issuecomment-5575546837)
overlays only this sidecar's `ArtifactRevision` and `ArtifactCollectionItem` source lineage to preserve
`content`, `external-skill-snapshot`, and `accepted-observation` in their stored
order. Scope and Source schemas remain unchanged. Head GET uses the pinned
upstream strong validator `"revision:N"`; exact `If-None-Match` equality returns
304 with ETag and request ID and no body. Exact revision GET does not substitute
the head or use its cache validator. Artifact create and replace remain
deferred, and these three GETs add no MCP tools.

`canonical/stats-manifest.json` projects only canonical `POST /v1/stats` into
`canonical/stats.json` and `api/canonical/stats`. It is the method-migrated
counterpart to frozen legacy `GET /v1/stats`, so it validates the exact
compatibility migration rather than appearing as an ordinary upstream-only
operation. The Server resolves the complete Scope selection before any SQLite
statistics reader runs, aggregates the frozen set, preserves `by_scope`, and
does not register an MCP tool. The raw fixed74 schema omits the Runtime's
redacted `404 scope_not_found` admission outcome. Generated clients therefore
report that result as unexpected; it must not be rewritten as `422` or hidden
by a synthetic generated response.

OpenAPI 3.0 cannot encode the combined `source_refs` + `artifact_refs` maximum
described by the Candidate schemas. The generator derives the affected model
set from the two `maxItems: 32` declarations and emits the supplemental
`api/v1.ValidatePowerContextContract` validator. HTTP, MCP, and the public Go
Client all invoke it at their decoded transport boundary.
