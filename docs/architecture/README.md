# Architecture

PowerContext Go is a contract-first modular monolith. It ports Python behavior,
not Python module layout: packages follow stable domain and ownership
boundaries, while files inside a package are grouped by one responsibility.

## Final directory structure

```text
powercontext-go/
├── .env.example              operator-ready frozen-default configuration
├── api/v1/                    generated OpenAPI wire types and contracts
├── artifact/                  immutable Artifact core and typed families
│   ├── experience/            experience generation, prompts, validation
│   ├── handoff/               handoff content, evidence, resolution, service
│   ├── memory/                authority model, extraction, search, indexing
│   └── skill/                 managed and external Skill behavior
├── benchmark/locomo/          operator config, documentation, ignored results
├── build/                     native release-asset manifest
├── client/                    typed client for all OpenAPI operations
├── cmd/powercontext/          single executable entrypoint
├── docs/                      architecture, ADRs, RFCs, release guidance
├── evaluation/                deployment-neutral Codex/SQLite evaluation control plane
├── inference/                 provider-neutral generation and embeddings
├── integrations/
│   ├── bub/                   retained Python host adapter
│   ├── claude-code/           retained Python Claude Code plugin
│   ├── codex/                 retained Python Codex plugin
│   ├── dsh/                   retained TypeScript DSH plugin
│   ├── hermes/                retained Python Hermes provider
│   ├── langgraph/             retained Python LangGraph package
│   ├── openclaw/              retained TypeScript OpenClaw memory plugin
│   ├── opencode/              retained TypeScript OpenCode plugin
│   ├── pi/                    retained TypeScript Pi extension
│   └── workbuddy/             retained Python WorkBuddy plugin
├── internal/
│   ├── benchmark/             bounded benchmark adapters and fixtures
│   ├── cli/                   Cobra command implementation
│   ├── contextpack/           bounded context assembly
│   ├── endpoint/              one application-operation boundary for HTTP/MCP
│   ├── handoffreport/         catalog, activity, selection, digest, rendering
│   ├── httpapi/               OpenAPI HTTP transport and middleware
│   ├── jcs/                   RFC 8785 canonical JSON boundary
│   ├── mcpapi/                fixed 20 + optional 4 MCP tool surface
│   ├── modelprovider/         concrete remote/local provider adapters
│   ├── observability/         privacy-safe logging, metrics, and tracing
│   ├── review/                Candidate generation/revision/approval domain
│   ├── runtime/               lifecycle, Scope gates, application orchestration
│   ├── scheduler/             interval scheduler and bounded APScheduler Pickle
│   ├── sqlstore/              relational stores, projections, and native DB adapters
│   │   ├── oceanbase/         OceanBase FTS/vector indexes
│   │   ├── schema/            embedded Python-compatible relational DDL
│   │   ├── seekdb/            embedded seekDB native loader
│   │   └── sqlitevec/         embedded sqlite-vec extension
│   ├── stats/                 statistics domain assembly
│   ├── webui/                 embedded Dashboard templates and assets
│   └── work/                  Work records and continuity projection
├── openapi/                   authoritative HTTP contract and generation hook
├── server/                    configuration and process composition root
├── source/                    Source adapters, values, catalog, journal
├── test/
│   ├── conformance/           frozen Python Oracle and compatibility evidence
│   ├── differential/          black-box Python/Go comparisons
│   └── e2e/                   process and backend vertical slices
├── tools/                     contract, fixture, smoke, benchmark, release tools
└── trigger/                   lifecycle-free trigger values
```

Top-level packages are public only when users or integrations need their types.
Concrete technology choices stay under `internal`; process startup stays in
`server` and `cmd`. This avoids both a Python-shaped `src` tree and a large
catch-all infrastructure package.

## Go-primary monorepo boundary

The Go Server, SDK, CLI, OpenAPI contract, and SQLite path are this
repository's primary product surface. `evaluation/` and `integrations/` remain
maintained, licensed, and tested auxiliary monorepo assets: the former is a
deployment-neutral Codex/SQLite evaluation control plane, while the latter
contains host-native assets that call the Go Server over HTTP or MCP. They are
not Go binary runtime dependencies or primary implementation languages.

Before WP6 acceptance, Codex, WorkBuddy, and SQLite are the only host/database
scope. The other retained adapters remain P3 assets with their own executable
CI job; they are not removed or treated as WP6 evidence. seekDB and OceanBase
remain the final P4 backend-alignment scope.

## Dependency direction

```text
source / artifact / trigger / inference
  → artifact families
  → internal/review / internal/contextpack / internal/stats
  → internal/work / internal/handoffreport
  → internal/runtime
  → internal/endpoint
  → internal/httpapi / internal/mcpapi / internal/webui
  → server / cmd
```

`source`, `artifact`, `trigger`, `inference`, their typed Artifact families,
`client`, and `server` are the deliberate public Go surface. Product-only
domains live under `internal` so their exported identifiers can remain useful
inside the repository without creating accidental external compatibility
promises. `client` depends on generated wire contracts, not domain internals.
Host
integrations depend on the published HTTP contract and never import Go domain
or persistence code. `internal/sqlstore` may depend on domain packages but not
on `internal/runtime`, server, or transports.

## Ownership and lifecycle

- Domain values validate at construction and copy mutable slices/maps at their
  boundaries.
- `internal/runtime.Runtime` admits operations, rejects new work during
  shutdown, drains active work, serializes exact-Scope writes, and leaves reads
  concurrent.
- `server.Application` is the process composition root. It opens concrete
  resources, builds use cases, exposes the shared endpoint, and closes owned
  resources in order.
- `internal/endpoint` is the only application-operation boundary. HTTP and MCP
  adapt into it directly; MCP never loops back through HTTP.

## Authority and transactions

Immutable Artifact revisions, Memory manifests/entry versions, and Source
journal state are authoritative. FTS and vector indexes are projections that
can be rebuilt from the authority state.

Inference and filesystem/provider calls occur outside SQL transactions. A
transaction is opened only for the final authority update: Candidate/Artifact
CAS, associated cursor CAS, and projection updates commit together. Stores
accept the narrow `DBTX` surface required by the use case; there is no generic
repository abstraction.

SQLite and embedded seekDB retain the Python `pc_*` schema and APScheduler
sidecar format. OceanBase uses explicit capability probing and backend-specific
FTS/vector implementations while preserving the same domain behavior.

## File organization

Large packages use cohesive same-package files instead of subpackages created
only to shorten files. Examples:

- Memory separates write orchestration, search/read behavior, and immutable
  value helpers.
- Handoff separates citations, resolved evidence, generation requests, content,
  and activation state while keeping their invariants in one domain package.
- Work keeps each immutable record beside its schema-specific codec; shared
  citation and validation rules remain package-local.
- Handoff Report separates catalog models, activity events, validation, JSON
  projection, selection, and rendering.
- External Skill separates configured targets, provider discovery, bounded
  frontmatter parsing, and TOCTOU-safe filesystem fingerprinting.
- LoCoMo separates ingestion, evaluation, checkpoint storage, metrics, and
  bounded concurrent execution.
- Model provider construction separates routing from OpenAI-compatible,
  native-SDK, and narrow HTTP protocol configuration.
- Scheduler separates the allowlisted Pickle job model, bounded reader, and
  protocol-5 writer.
- Server separates storage opening, process foundations, repositories, service
  assembly, and high-level application orchestration.

This keeps unexported invariants shared without adding import cycles or
artificial `common`, `models`, `services`, or `repositories` packages.

## Contract and compatibility controls

- `openapi/powercontext.yaml` is the HTTP truth; `api/v1` is generated.
- Prompt files are embedded from each family's `prompts` directory and checked
  by frozen SHA-256 fixtures.
- `test/conformance` freezes the Python commit, schemas, prompts, provider
  matrix, SQLite/sqlite-vec/scheduler fixtures, and exact digest behavior.
- `tools/process-smoke` executes the built binary through CLI, HTTP, auth,
  Dashboard, MCP 20+4, restart persistence, and graceful shutdown.
- `tools/locomo` runs or resumes the real LoCoMo pipeline while benchmark
  schemas, metrics, prompts, and the frozen dataset remain under
  `internal/benchmark/locomo`.
- Generated-contract checks fail on OpenAPI, MCP schema, client invocation, DSH
  operation, or traceability drift.
- Repository conformance checks reject accidental public packages and imports
  that reverse the documented domain, runtime, persistence, endpoint, or
  transport dependency direction.
- Compatibility changes to persistence, lifecycle, package direction, or host
  boundaries require an ADR.

The [compatibility evidence matrix](compatibility-evidence.md) maps each
independent compatibility surface to its source of truth and required gate.

The [backend release audit ledger](backend-audit-ledger.md) classifies the
pinned v0.1.0 persistence cases into current SQLite, shared, seekDB-specific,
and OceanBase-specific evidence before the two final backend phases.
