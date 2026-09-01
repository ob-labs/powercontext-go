# Embedding Dimension and Readiness Alignment Design

Status: approved architecture, pending implementation planning
Date: 2026-09-01
Tracking issue: ob-labs/powercontext-go#3, WP1 v0.1.0 core release delta
Go source review base: `2f927747d3ff3f0bcbabee2a21db4fe3961e44ae`
Upstream release target: `oceanbase/powercontext@7b736206a53a6de6f43d4b517893ee1a80e7183d`

## Purpose

PowerContext Go records one configured embedding dimension in its embedding
profile and validates returned vectors against that dimension. The dimension
does not currently cross the public `EmbeddingTransport` boundary, so built-in
providers may use their own defaults. The SQLite vector projection also reports
an existing-table dimension mismatch as a generic probe failure.

This design aligns the observable Go behavior with the pinned PowerContext
v0.1.0 release by:

- carrying the configured dimension through every embedding provider boundary;
- exposing only a strict, redacted provider-rejection reason through readiness;
- keeping SQLite projection dimension mismatch as an actionable startup failure;
- preserving safe typed errors and underlying causes without leaking free-form
  provider, SQL, database, or Memory data;
- mapping the corresponding release cases only after case-specific evidence is
  executable on the exact submitted Head.

## Approved decisions

The following decisions are final for implementation:

1. Introduce an inference-owned `EmbeddingRequest` and change
   `EmbeddingTransport.Embed` to accept that request.
2. `BatchedEmbeddingModel` constructs every request from its one fixed
   embedding profile. Provider transports do not own or reload the profile.
3. Provider HTTP rejection is public in readiness only for a strict set of
   non-transient HTTP 4xx statuses and only as a status-code-only reason.
4. SQLite existing-table dimension mismatch prevents Application construction.
   It does not create a degraded Application solely to expose `/health/ready`.
5. SQLite never automatically drops or rebuilds an incompatible vector table.
6. A stale SQLite probe row is removed before the next probe, and cleanup is
   attempted after the query regardless of query success.
7. SQLite capability errors retain safe public classification and underlying
   root causes through an internal multi-cause wrapper.
8. Delivery is split into two sequential PRs: provider/readiness first, then
   SQLite projection lifecycle after the first PR is merged and verified.
9. OceanBase and seekDB dimension migration remain in the final P4 backend plan.

## Current architecture and defect

The current path is:

```text
Server inference config
    -> EmbeddingProfile
    -> BatchedEmbeddingModel
    -> EmbeddingTransport.Embed(ctx, texts, inputType)
    -> provider request
    -> ProviderEmbeddingResult
    -> BatchedEmbeddingModel validates vector dimension
    -> Memory projection
```

The profile dimension is used only after the provider responds. A provider that
supports output-dimension selection can therefore receive no dimension and
return vectors at its default size. The model then reports an invalid result,
even though the configured dimension could have been sent on the request.

The SQLite vector index currently executes:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS pc_memory_entry_vec
USING vec0(embedding float[N])
```

When the table already exists with another `N`, `IF NOT EXISTS` succeeds. The
dimension mismatch is discovered indirectly during the later probe and is
reported as `sqlite-vec probe failed`.

## Alternatives considered

### A. Inference-owned request value — selected

Carry inputs, input type, and dimension in one validated request value. This
makes the relationship explicit and keeps one dimension source.

### B. Add a fourth positional dimension argument — rejected

This is a smaller signature diff but continues an increasingly fragile
positional contract and makes wrappers more likely to omit or reorder fields.

### C. Store dimension in each provider transport constructor — rejected

This duplicates the embedding profile in transport state. The model could
validate one dimension while the provider sends another.

## EmbeddingRequest public contract

The inference package adds a public value type following the same immutable
copying pattern as `ProviderEmbeddingResult`:

```go
type EmbeddingRequest struct {
    inputs    []string
    inputType EmbeddingInputType
    dimension int
}

func NewEmbeddingRequest(
    inputs []string,
    inputType EmbeddingInputType,
    dimension int,
) (EmbeddingRequest, error)

func (r EmbeddingRequest) Inputs() []string
func (r EmbeddingRequest) InputType() EmbeddingInputType
func (r EmbeddingRequest) DimensionCount() int
```

The constructor:

- clones the input slice;
- accepts only `EmbeddingDocument` and `EmbeddingQuery`;
- requires a positive dimension;
- returns a stable inference configuration error for invalid input type or
  dimension;
- does not read environment variables or provider configuration.

`Inputs()` returns a clone, matching the existing result value behavior.

The transport contract becomes:

```go
type EmbeddingTransport interface {
    Embed(context.Context, EmbeddingRequest) (ProviderEmbeddingResult, error)
}
```

`BatchedEmbeddingModel` remains responsible for batching. For every batch it
constructs an `EmbeddingRequest` from:

- the cloned batch inputs;
- `EmbeddingDocument`, matching the existing public model behavior;
- `m.profile.DimensionCount()`.

Direct provider consumers and provider wire tests may construct
`EmbeddingQuery` requests through the same public constructor. This design does
not add a query method to `BatchedEmbeddingModel`.

Tracing and usage-reporting wrappers receive and forward the complete request
without reconstructing it.

## Public API compatibility

Changing `EmbeddingTransport.Embed` is a deliberate pre-release public Core
extension change. The implementation must:

- update every in-repository implementation and fake in the same PR;
- update the approved pre-release `apidiff` baseline deliberately;
- rebuild and run the isolated downstream public consumer;
- add an external consumer transport implementation that reads the new request;
- describe the interface change explicitly in the PR body;
- avoid a temporary V1/V2 adapter, optional-interface detection, or two parallel
  transport contracts.

The repository has not published its first formal Go release. A compatibility
layer would preserve the exact duplicate-dimension state this design removes.

## Provider mappings

Each built-in provider maps `EmbeddingRequest.DimensionCount()` as follows:

| Provider | Wire or SDK field | Rule |
| --- | --- | --- |
| OpenAI | `dimensions` | Send the configured dimension on every embedding request. |
| Google | `output_dimensionality` | Convert to the SDK `int32` field with an explicit overflow check. |
| Cohere | `output_dimension` | Send through the Cohere v2 request. |
| VoyageAI | `output_dimension` | Send in the JSON request body. |
| Sentence Transformers | `truncate_dim` semantics | Truncate local output before common dimension validation; reject a request larger than native output. |
| Bedrock Titan v2 | `dimensions` | Send only for the v2 request shape. |
| Bedrock Cohere v4 | `output_dimension` | Send only for the v4 request shape. |
| Bedrock Nova | `embeddingDimension` | Send only for Nova embeddings. |
| Bedrock Titan v1 and Cohere v3 | no override | Keep fixed-dimension behavior and validate the returned dimension. |

Provider adapters must not clamp, default, guess, or silently narrow dimension.
An integer conversion that cannot be represented by the provider SDK field is
a stable local configuration error.

An unrecognized Bedrock model family must not be treated as dimension-aware by
default. Support is added only with an explicit request-shape branch and test.

## Provider rejection classification

`ConfigurationError` already retains a safe code/detail and an optional cause
whose text is not included in `Error()`. Provider adapters use that contract.

For stable configuration rejections the error is:

```text
code:   provider-rejected
detail: HTTP NNN
```

The cause may remain available through `errors.Is` or `errors.As`, but the
provider body, URL, request, credential, model payload, Source, Memory, prompt,
and SDK object string must not appear in `Error()` or readiness JSON.

The public non-transient set includes statuses such as:

```text
400 401 403 404 405 406 410 413 415 422
```

The transient set remains unavailable rather than misconfigured:

```text
408 425 429
```

All 5xx responses, network errors, TLS errors, timeouts, and cancellation retain
their existing unavailable, timeout, or cancellation semantics.

Provider-specific response bodies are never used to decide readiness text.

## Readiness status contract

Existing exact statuses remain valid:

```text
ready
unavailable
timeout
misconfigured
```

One strict reason form is added:

```text
misconfigured: provider-rejected (HTTP NNN)
```

`DependencyProbe` returns that form only when:

- the error matches `*inference.ConfigurationError`;
- `Code()` is exactly `provider-rejected`;
- `Detail()` exactly matches a permitted non-transient `HTTP 4NN` status;
- the constructed value passes the strict `CheckStatus` parser.

Every other configuration error returns plain `misconfigured`, even if its
detail contains text. `CheckStatus.valid()` must not accept arbitrary strings
that merely start with `misconfigured:`.

The public `/health/ready` response is tested with provider bodies containing a
credential, URL, and private Memory-like content. None may appear in the body,
error representation, test diagnostics, or logs owned by this change.

## SQLite vector schema inspection

`SQLiteMemoryVectorIndex.Initialize` changes to this sequence:

```text
SELECT vec_version()
    -> validate exact supported sqlite-vec version
create ordinary projection metadata table
inspect sqlite_master for pc_memory_entry_vec
    table absent:
        create vec0 with configured dimension
        inspect the created schema
    table present:
        parse embedding float[N]
compare existing N with configured dimension
clear stale rowid -1
insert probe row
query probe row
attempt cleanup regardless of query outcome
validate returned rowid
```

The schema parser accepts only the repository-owned vec0 table shape. It does
not execute or log DDL. A same-name ordinary table, a different column shape,
or an unparseable virtual-table declaration returns a fixed incompatible-schema
reason.

## SQLite mismatch behavior

An existing dimension mismatch returns a Memory capability error with:

```text
Capability: vector
Detail: sqlite-vec projection dimension 1536 does not match configured dimension 768; rebuild the projection
```

Only the two dimensions and fixed guidance are public. The error must not
contain:

- raw DDL or SQL;
- database path or URL;
- SQL parameters;
- scope or artifact identifiers;
- Memory contents;
- embedding bytes or vectors;
- connection or extension internals.

An unrecognized schema returns:

```text
sqlite-vec projection schema is incompatible; rebuild the projection
```

The mismatch occurs during repository initialization. Application construction
fails and no HTTP or MCP Server begins serving. The implementation does not
construct a degraded Application solely to expose this startup failure through
`/health/ready`.

The Issue wording is interpreted as release-readiness/startup-gate evidence for
SQLite mismatch, not as a requirement to manufacture an HTTP readiness response
from an Application that failed to initialize.

## SQLite probe lifecycle

The fixed probe row remains `rowid = -1`.

Initialization first deletes any stale `rowid = -1`, then inserts and queries a
new probe. Cleanup is attempted after the query even when the query fails.

The implementation preserves both query and cleanup errors. Cleanup never
replaces the first root failure, and a query failure never skips cleanup.

No mismatch path automatically drops, truncates, rebuilds, or migrates the
vector table. Projection rebuild remains an explicit later operation and does
not rewrite authoritative Memory revisions.

## SQLite multi-cause error

The public `memory.CapabilityNotSupportedError` layout is unchanged. SQLStore
adds an internal wrapper:

```go
type sqliteVecCapabilityFailure struct {
    public *memory.CapabilityNotSupportedError
    causes []error
}

func (e *sqliteVecCapabilityFailure) Error() string
func (e *sqliteVecCapabilityFailure) Unwrap() []error
```

`Error()` delegates only to the safe public error. `Unwrap()` exposes the safe
typed error and non-nil underlying causes. Therefore:

```go
errors.As(err, &capabilityError)
errors.Is(err, rootCause)
```

both work, while `err.Error()` cannot leak the root-cause text.

This wrapper is internal to SQLStore and does not change the public Memory API
baseline.

## Delivery plan

### PR 1: provider dimension and readiness

Suggested title:

```text
feat(inference): pass embedding dimensions to providers
```

Owned scope:

- `inference/embedding.go` and tests;
- inference tracing and usage wrappers;
- built-in OpenAI, Google, Cohere, VoyageAI, Bedrock, and Sentence Transformers
  transports and their wire tests;
- Runtime readiness classification and tests;
- Server public readiness tests;
- pre-release public API baseline and isolated downstream consumer;
- case-specific parity rules and generated inventory for the corresponding
  provider, composition, readiness, and Server release cases.

Excluded from PR 1:

- SQLite schema inspection or probe changes;
- OceanBase or seekDB;
- OpenAPI, CLI, or new environment settings;
- retained host adapters.

### PR 2: SQLite projection drift and probe lifecycle

Suggested title:

```text
fix(sqlite): diagnose embedding projection dimension drift
```

PR 2 starts from current main only after PR 1 is merged and its exact-main five
workflow groups are successful.

Owned scope:

- SQLite vector schema inspection and dimension comparison;
- stale probe cleanup;
- multi-cause capability errors;
- real SQLite and Server construction tests;
- case-specific parity rules and generated inventory for the four SQLite vector
  release cases;
- one concise learned rule if implementation uncovers a reusable bug class.

Excluded from PR 2:

- provider request changes already delivered in PR 1;
- automatic projection rebuild;
- authoritative Memory migrations;
- OceanBase or seekDB behavior.

Sequential delivery prevents two open branches from rewriting the same parity
inventory and invalidating each other's Base evidence.

## TDD requirements for PR 1

Before production changes, tests must fail for:

1. missing profile dimension in the request passed to a transport;
2. a tracing or usage wrapper dropping the request dimension;
3. missing OpenAI `dimensions`;
4. missing Google `output_dimensionality` or unchecked narrowing;
5. missing Cohere `output_dimension`;
6. missing VoyageAI `output_dimension`;
7. missing Sentence Transformers truncation;
8. requested local dimension larger than native output;
9. missing supported Bedrock dimension field;
10. an override sent to a fixed-dimension Bedrock model;
11. a non-transient provider 4xx classified as unavailable;
12. `408`, `425`, or `429` classified as misconfigured;
13. arbitrary configuration detail accepted as a readiness reason;
14. provider body, URL, or credential appearing in readiness;
15. an external transport implementation still compiling against the old method.

Provider tests exercise the real SDK request value or serialized JSON. They do
not assert only that an internal helper received a dimension.

## Mutation proof for PR 1

The suite must reject these mutants:

- remove the dimension field from any supported provider request;
- narrow Google dimension without an overflow guard;
- send an override to Titan v1 or Cohere v3;
- validate Sentence Transformers output before required truncation;
- classify HTTP 429 as `provider-rejected`;
- append provider response text to `ConfigurationError.Detail`;
- accept arbitrary `misconfigured: ...` values;
- drop dimension in a wrapper or after the first batch.

## TDD requirements for PR 2

Real sqlite-vec tests establish and then exercise:

- a matching vec0 dimension;
- a mismatching vec0 dimension;
- an incompatible same-name schema;
- a stale `rowid = -1`;
- query and cleanup failures;
- an oversized fresh dimension rejected by sqlite-vec;
- an authoritative Memory sentinel that must survive initialization failure.

The tests prove:

- safe public detail and absence of DDL/path/Memory/vector data;
- `errors.As` matches `*memory.CapabilityNotSupportedError`;
- `errors.Is` matches each injected or native root cause;
- cleanup occurs after query failure;
- no mismatch path drops or rewrites authoritative data;
- Application construction fails before the Server serves.

Tests compare SQLite schema and row semantics, not raw database bytes.

## Validation gates

PR 1 local and CI evidence includes:

```text
go test -count=1 ./inference
go test -count=1 ./internal/modelprovider
go test -count=1 ./internal/runtime -run readiness
go test -count=1 ./server -run readiness
make api-compat
make contract-test
make lint
go test -race -count=1 ./inference ./internal/runtime
exact-upstream parity inventory check
```

PR 2 local and CI evidence includes:

```text
go test -count=1 ./internal/sqlstore -run SQLiteVec
go test -count=1 ./server -run Embedding
make test-sqlite
make lint
go test -race -count=1 ./internal/sqlstore
exact-upstream parity inventory check
```

On Windows, missing `sqlite3.h` does not authorize disabling CGO, replacing real
schema behavior with mocks, or claiming the Server suite passed. Parser/helper
tests may run locally; supported Linux exact-Head CI supplies real sqlite-vec,
Server, race, and full-suite evidence.

Every implementation PR must reconcile:

```text
GitHub changedFiles
REST unique filenames
local current-Base...Head diff headers
```

Every changed Head or Base invalidates prior diff, test, review, and CI evidence.
Merge is verified through server-side merge metadata, followed by exact-main
Main, E2E, CodeQL, License, and Windows workflow success before Issue updates.

## Parity evidence

The target cluster contains ten currently pending v0.1.0 cases:

- two composition embedding cases;
- two dependency readiness cases;
- one provider rejection case;
- one Server readiness case;
- four SQLite vector-index cases.

PR 1 owns the first six. PR 2 owns the four SQLite cases. Counts are updated
only by the exact parity generator after evidence resolves; the design does not
manually promise a mapped/pending total.

## Security and privacy invariants

- No provider body, URL, credential, prompt, Source, Memory, vector, scope ID,
  database URL, or local path is added to readiness or stable errors.
- Provider status is public only as a permitted numeric HTTP code.
- SQLite mismatch detail contains only dimensions and fixed guidance.
- Underlying causes remain matchable but are never interpolated into stable
  `Error()` values.
- No new secret-bearing configuration or environment variable is introduced.
- No provider or database operation logs the same error a second time.

## Acceptance criteria

The WP1 embedding core release-delta item can be checked only after:

1. PR 1 and PR 2 are both merged in sequence;
2. each final PR Head completes all exact-Head required checks;
3. each merge commit completes the five exact-main workflow groups;
4. provider wire tests prove every supported dimension field;
5. readiness exposes only the approved redacted reason;
6. SQLite mismatch is an actionable startup failure with no automatic drop;
7. stale probe cleanup and root-cause preservation are proven;
8. all ten upstream cases have resolvable case-specific evidence;
9. Issue #3 is updated from live main evidence, not from an open PR.

The wider 812-case mapping item remains open until every remaining case is
resolved. Completing this design does not complete WP1 by itself.
