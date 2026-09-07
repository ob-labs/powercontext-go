# OpenAPI

`powercontext.yaml` is copied from the current Python reference baseline and is
the sole source of truth for the generated legacy HTTP wire surface.

`compatibility-surface.json` pins the later upstream operation inventory while
the corresponding use cases are implemented incrementally. Generation validates
the inventory without treating it as a second OpenAPI document. This preserves
the public legacy `v1.Handler`, `v1.Invoker`, `v1.NewServer`, `v1.NewClient`,
and `GET /v1/stats` contracts. It also reserves `GetStatsCanonical` for the
future canonical `POST /v1/stats` operation and records the 14 retained Go
Handoff Report extensions. `mcp_generated_openapi_operations` enumerates only
the generated OpenAPI-dispatch MCP tools; native conditional tools such as the
Handoff picker and Scope Binding tools are registered and tested separately. A
schema inventory entry alone does not expose a generated OpenAPI tool.

OpenAPI 3.0 cannot encode the combined `source_refs` + `artifact_refs` maximum
described by the Candidate schemas. The generator derives the affected model
set from the two `maxItems: 32` declarations and emits the supplemental
`api/v1.ValidatePowerContextContract` validator. HTTP, MCP, and the public Go
Client all invoke it at their decoded transport boundary.
