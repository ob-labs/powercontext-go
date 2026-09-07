# API generator

Generation tooling for `api/v1` belongs here. Generated output derives only
from the legacy `openapi/powercontext.yaml` contract.

`openapi/compatibility-surface.json` is a pinned inventory for the later
upstream snapshot. The generator verifies it before regenerating the legacy
surface: the 14 retained Go Handoff Report operations stay available, while
the 38 upstream-only operations remain unimplemented. In particular, legacy
`GET /v1/stats` keeps `GetStats`; the future canonical `POST /v1/stats` is
reserved as `GetStatsCanonical`. The inventory is a migration gate, not an
alternate OpenAPI input, so it cannot expand `v1.Handler` or `v1.Invoker`.
Its `mcp_generated_openapi_operations` field covers only schema-generated
OpenAPI-dispatch tools. Native conditional MCP tools have their own registration
and tests.

The Scope sidecar is the first deliberately narrow canonical package. Its
`-scope-sidecar-manifest` mode reads the raw pinned upstream OpenAPI,
compatibility inventory, and frozen legacy document; it validates the complete
77-operation ledger before projecting only the five Scope read/resolve
operations into `api/canonical/scopes`. The manifest carries the sole product
overlay, which limits `ScopeBindingKey.integration` to `codex` and `workbuddy`.
The mode requires package `scopes` below a `canonical/scopes` target and rejects
every Client Invoker output. It generates no route registration, MCP schema, or
legacy `v1` artifact.

The independent `-artifact-sidecar-manifest` mode projects exactly
`get_artifact` and `get_artifact_revision` into `api/canonical/artifacts`.
It pins the complete upstream digest and operation ledger, closes only the
required components, and applies the Artifact-only lineage enum policy tracked
in Issue #202. Its generated artifacts are listed in `test/generator-inventory.json`.
Projection tests reject pin, digest, operation, ledger, and policy mutants and
verify the complete generated output in a fresh consumer module.

The frozen OpenAPI 3.0 document uses `$ref` with a `nullable: true` sibling.
OpenAPI 3.0 formally ignores `$ref` siblings, so the generator command creates
an ephemeral, semantically equivalent `oneOf: [$ref, null]` view before invoking
ogen. The canonical document is neither rewritten nor checked in twice.

The command also generates the PowerContext semantic validator for cross-field
Candidate evidence limits. Its model list is discovered from the canonical
schema using the same rule as the frozen Python generator, so this exception
to plain OpenAPI validation remains deterministic and reviewable.
