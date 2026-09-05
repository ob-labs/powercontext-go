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
│   ├── codex/                 supported Codex plugin
│   ├── workbuddy/             supported WorkBuddy plugin
│   └── other roots            historical adapter source, not shipped or active in CI
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
│   │   ├── oceanbase/         historical unsupported backend source
│   │   ├── schema/            embedded Python-compatible relational DDL
│   │   ├── seekdb/            historical unsupported backend source
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
maintained and licensed auxiliary monorepo assets: the former is a
deployment-neutral Codex/SQLite evaluation control plane, while the latter
contains supported Codex/WorkBuddy assets plus historical source. Only Codex
and WorkBuddy are active, packaged host integrations. These assets are not Go
binary runtime dependencies or primary implementation languages.

Codex, WorkBuddy, and SQLite are the only supported host/database scope. Other
adapter and backend source is retained only for history and comparison; it has
no active CI, release, installation, or runtime support contract.

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

SQLite retains the Python `pc_*` schema and APScheduler sidecar format. Legacy
seekDB and OceanBase code is outside the supported runtime and release matrix.

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
- Generated-contract checks fail on OpenAPI, MCP schema, client invocation, or
  traceability drift.
- Repository conformance checks reject accidental public packages and imports
  that reverse the documented domain, runtime, persistence, endpoint, or
  transport dependency direction.
- Compatibility changes to persistence, lifecycle, package direction, or host
  boundaries require an ADR.

The [compatibility evidence matrix](compatibility-evidence.md) maps each
independent compatibility surface to its source of truth and required gate.

The [backend release audit ledger](backend-audit-ledger.md) remains historical
analysis of the pinned v0.1.0 persistence cases; only its SQLite evidence is in
the supported product matrix.
