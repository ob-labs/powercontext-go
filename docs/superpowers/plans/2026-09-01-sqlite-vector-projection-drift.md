# SQLite Vector Projection Drift Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect an existing sqlite-vec projection dimension/schema mismatch before probing, reject fresh profiles above the embedded sqlite-vec limit, preserve safe typed and underlying errors, clean stale probe rows, and fail Server startup without rewriting authoritative Memory data.

**Architecture:** `SQLiteMemoryVectorIndex.Initialize` inspects only the owned vec0 projection schema before creating or probing it. An internal wrapper presents a safe `memory.CapabilityNotSupportedError` while unwrapping root causes for matching. Projection mismatch remains an Application-construction failure; no degraded Application, automatic drop, migration, or rebuild is introduced.

**Tech Stack:** Go 1.27, CGO SQLite with embedded sqlite-vec v0.1.9, `database/sql`, `errors.Join`/multi-unwrapping, Server application composition, parity inventory generator.

**Spec:** `docs/superpowers/specs/2026-09-01-embedding-dimension-readiness-design.md`

## Global Constraints

- Start from current `origin/main`; any changed Base or Head invalidates previous diff, test, review, and CI evidence.
- Load complete Modern Go guidance before every Go edit.
- Use TDD: every new behavior starts with a focused RED execution, then a minimal GREEN repair.
- The vec0 table is a rebuildable projection, but this plan never automatically drops, truncates, rebuilds, or migrates it.
- Do not modify authoritative Memory revisions, manifests, content, SQL scope identities, provider behavior, OpenAPI, CLI settings, or environment settings.
- Existing vec0 schema dimension mismatch prevents `OpenApplication` from returning an Application; do not construct a degraded Server solely for `/health/ready`.
- The embedded sqlite-vec v0.1.9 maximum is 8192 float dimensions; reject a fresh larger configured profile before creating a projection and do not parse extension error text.
- Public mismatch details may contain only existing/configured numeric dimensions and fixed rebuild guidance.
- Do not expose raw DDL, SQL, bind values, database paths/URLs, Memory content, vectors, scope IDs, or root-cause text in stable errors.
- Underlying root causes must remain matchable with `errors.Is`; safe public capability classification must remain matchable with `errors.As`.
- `rowid = -1` remains the probe identity. Remove stale `-1` before probing and attempt cleanup after every query outcome.
- SQLite raw bytes are not a fixture assertion target; tests compare schema and row semantics.
- OceanBase and seekDB remain final P4 work and must not change in this PR.
- Windows missing `sqlite3.h` is a validation gap, never authorization to disable CGO or replace real sqlite-vec tests with mocks. Linux exact-Head CI is required for real index and Server proof.
- Update generated parity inventory only through `tools/parity-inventory-generate`; never hand-edit its counts.
- All commits use the repository Lore format and `Co-authored-by: OmX <omx@oh-my-codex.dev>`.

---

### Task 1: Define owned vec0 schema parsing and safe multi-cause failures

**Files:**
- Modify: `internal/sqlstore/sqlite_memory_vector.go`
- Modify: `internal/sqlstore/sqlite_memory_vector_test.go`
- Create: `internal/sqlstore/sqlite_memory_vector_schema_test.go`

**Interfaces:**
- Produces: `parseSQLiteVecProjectionDimension(string) (int, bool)` as an unexported parser for the exact owned vec0 DDL shape.
- Produces: `sqliteVecCapabilityFailure` with `Error() string` and `Unwrap() []error`.
- Produces: `newSQLiteVecCapabilityFailure(detail string, causes ...error) error`.
- Preserves: `errors.As(err, *memory.CapabilityNotSupportedError)` and existing `Capability == "vector"` classification.

- [x] **Step 1: Write failing exact-schema parser tests**

Create `sqlite_memory_vector_schema_test.go` with hand-derived DDL literals:

```go
func TestParseSQLiteVecProjectionDimensionAcceptsOwnedSchema(t *testing.T) {
    dimension, ok := parseSQLiteVecProjectionDimension(
        "CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[1536])",
    )
    if !ok || dimension != 1536 {
        t.Fatalf("dimension, ok = %d, %t", dimension, ok)
    }
}

func TestParseSQLiteVecProjectionDimensionRejectsForeignSchema(t *testing.T) {
    for _, schema := range []string{
        "CREATE TABLE pc_memory_entry_vec (embedding BLOB)",
        "CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(other float[3])",
        "CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[0])",
        "CREATE VIRTUAL TABLE another_vec USING vec0(embedding float[3])",
        "untrusted DDL with /private/path and embedding float[3]",
    } {
        if dimension, ok := parseSQLiteVecProjectionDimension(schema); ok || dimension != 0 {
            t.Fatalf("accepted schema %q as %d", schema, dimension)
        }
    }
}
```

Use exact complete string matching or a regex anchored to the complete owned DDL; never extract `float[N]` from arbitrary DDL text.

- [x] **Step 2: Run parser tests and verify RED**

Run:

```text
go test -count=1 ./internal/sqlstore -run '^TestParseSQLiteVecProjectionDimension'
```

Expected: FAIL because the parser is not defined.

- [x] **Step 3: Write failing multi-cause safe-error tests**

Add tests with independent sentinel causes:

```go
func TestSQLiteVecCapabilityFailureMatchesSafeCapabilityAndRootCauses(t *testing.T) {
    queryCause := errors.New("query cause /private/db API_KEY=secret")
    cleanupCause := errors.New("cleanup cause Memory private text")
    err := newSQLiteVecCapabilityFailure("sqlite-vec probe failed", queryCause, cleanupCause)

    var capability *memory.CapabilityNotSupportedError
    if !errors.As(err, &capability) || capability.Capability != "vector" {
        t.Fatalf("capability = %#v, err = %v", capability, err)
    }
    if !errors.Is(err, queryCause) || !errors.Is(err, cleanupCause) {
        t.Fatalf("root causes are not matchable: %v", err)
    }
    for _, secret := range []string{"/private/db", "API_KEY=secret", "Memory private text"} {
        if strings.Contains(err.Error(), secret) {
            t.Fatalf("safe error leaked %q: %v", secret, err)
        }
    }
}
```

Add a direct mismatch-detail test requiring:

```text
sqlite-vec projection dimension 1536 does not match configured dimension 768; rebuild the projection
```

and rejecting raw DDL/path/vector sentinel text.

- [x] **Step 4: Run safe-error tests and verify RED**

Run:

```text
go test -count=1 ./internal/sqlstore -run '^TestSQLiteVecCapabilityFailure|^TestSQLiteVecProjectionDimensionMismatchDetail'
```

Expected: FAIL because the internal wrapper and safe mismatch constructor are absent.

- [x] **Step 5: Implement parser and internal wrapper**

Use a package-level anchored regex or an exact parser that accepts only:

```text
CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[N])
```

where `N` is a positive decimal integer with no signs, whitespace variants, or
additional columns.

Implement:

```go
type sqliteVecCapabilityFailure struct {
    public *memory.CapabilityNotSupportedError
    causes []error
}

func (e *sqliteVecCapabilityFailure) Error() string { return e.public.Error() }

func (e *sqliteVecCapabilityFailure) Unwrap() []error {
    values := make([]error, 0, 1+len(e.causes))
    values = append(values, e.public)
    values = append(values, e.causes...)
    return values
}
```

Filter nil causes. Return the bare public capability error only when no root
cause exists; return the wrapper otherwise. The public error type and fields
remain unchanged.

Add helpers that construct only the two fixed safe schema messages:

```go
func sqliteVecDimensionMismatchError(existing, configured int, causes ...error) error
func sqliteVecIncompatibleSchemaError(causes ...error) error
```

- [x] **Step 6: Run Task 1 tests and verify GREEN**

Run:

```text
go test -count=1 ./internal/sqlstore -run 'TestParseSQLiteVecProjectionDimension|TestSQLiteVecCapabilityFailure|TestSQLiteVecProjectionDimensionMismatchDetail'
go vet ./internal/sqlstore
```

Expected: PASS on a supported CGO SQLite host. If Windows cannot compile the
package because `sqlite3.h` is missing, run parser/wrapper tests using the
repository-supported temporary matching header include and record the command;
the exact Linux CI remains the full proof.

- [x] **Step 7: Commit schema/error primitives**

```text
git add internal/sqlstore/sqlite_memory_vector.go internal/sqlstore/sqlite_memory_vector_test.go internal/sqlstore/sqlite_memory_vector_schema_test.go
git commit \
  -m "Diagnose sqlite vector projection schema drift" \
  -m "Owned vec0 schema parsing now distinguishes an incompatible or dimension-mismatched projection from a generic probe failure, while safe capability classification and root-cause matching are both preserved." \
  -m "Constraint: Stable errors expose only dimensions and rebuild guidance; raw DDL and root-cause text remain private" \
  -m "Tested: exact schema parser and safe multi-cause error tests" \
  -m "Not-tested: real vec0 initialization lifecycle is delivered by the next task" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 2: Initialize vec0 safely, detect dimension drift, and clean probe rows

**Files:**
- Modify: `internal/sqlstore/sqlite_memory_vector.go`
- Modify: `internal/sqlstore/sqlite_memory_vector_test.go`

**Interfaces:**
- Consumes: `parseSQLiteVecProjectionDimension`, `sqliteVecDimensionMismatchError`, `sqliteVecIncompatibleSchemaError`, and safe multi-cause wrapper from Task 1.
- Produces: `SQLiteMemoryVectorIndex.Initialize` that validates existing projection dimension before probe, rejects a fresh oversized profile, and cleans stale probe rows.
- Preserves: exact sqlite-vec version v0.1.9, `rowid = -1`, current projection metadata table, and vector search behavior.

- [x] **Step 1: Add failing real SQLite dimension mismatch tests**

Use `OpenSQLite` with a temporary file-backed database so initialization can
run twice across independent transactions. Create profile dimension `3`, call
`Initialize`, then create profile dimension `4` and call `Initialize` again.

Require:

```go
var capability *memory.CapabilityNotSupportedError
if !errors.As(err, &capability) || capability.Capability != "vector" {
    t.Fatalf("err = %v", err)
}
if got := err.Error(); !strings.Contains(got, "dimension 3 does not match configured dimension 4; rebuild the projection") {
    t.Fatalf("err = %q", got)
}
```

Assert the error omits the database path, `CREATE VIRTUAL TABLE`, a Memory
sentinel, and an embedding/vector sentinel. Assert existing schema remains
`float[3]`; do not expect a new `float[4]` table.

- [x] **Step 2: Add failing incompatible-schema, fresh-oversized, and stale-probe tests**

Create a same-name ordinary table:

```sql
CREATE TABLE pc_memory_entry_vec (embedding BLOB)
```

Require the fixed incompatible-schema/rebuild message and no raw SQL text.

For stale probe cleanup, initialize a valid index, insert `rowid = -1` with a
valid packed vector, then call `Initialize` again and assert exactly zero
`rowid = -1` rows remain after successful probing.

For a fresh profile dimension `65536`, require the fixed safe detail
`sqlite-vec supports at most 8192 dimensions; configured dimension 65536 is unsupported`,
assert that no vec0 projection exists, and reject raw extension text or the
word `migrate` in the public error.

- [x] **Step 3: Add failing query/cleanup cause-preservation tests**

Introduce a narrow `DBTX` wrapper in the test file that delegates to the real
database except for the probe query and/or delete statement. The probe must use
`QueryContext`, rather than `QueryRowContext`: `DBTX.QueryRowContext` returns
the concrete `*sql.Row` type and cannot expose an injected sentinel from a
test wrapper. Return two sentinel errors only for these exact query strings:

```go
const probeQuery = "SELECT rowid FROM pc_memory_entry_vec WHERE embedding MATCH ? AND k = 1"
const probeDelete = "DELETE FROM pc_memory_entry_vec WHERE rowid = ?"
```

Require `errors.Is(err, queryCause)` and `errors.Is(err, cleanupCause)`, plus
safe public `CapabilityNotSupportedError` matching and no sentinel text in
`err.Error()`.

- [x] **Step 4: Run lifecycle tests and verify RED**

Run:

```text
go test -count=1 ./internal/sqlstore -run 'TestSQLiteVec.*(DimensionMismatch|IncompatibleSchema|StaleProbe|Probe.*Cause)'
```

Expected: current initialization either returns generic `sqlite-vec probe
failed`, leaves stale probe behavior unproven, or loses one of the root causes.

- [x] **Step 5: Refactor Initialize into schema and probe phases**

Keep version validation first. Keep creating `pc_memory_vector_entries` with
`CREATE TABLE IF NOT EXISTS`. Replace unconditional vec0 `CREATE ... IF NOT
EXISTS` with:

1. query `sqlite_master` for `type` and `sql` where name is
   `pc_memory_entry_vec`;
2. if absent, create the exact owned vec0 schema using the configured
   dimension, then query `sqlite_master` again;
3. if type is not `table` or the DDL parser rejects the SQL, return
   `sqliteVecIncompatibleSchemaError` with the underlying database cause only;
4. if parsed dimension differs, return `sqliteVecDimensionMismatchError`;
5. after confirming the pinned sqlite-vec version but before any projection DDL,
   reject a fresh configured dimension above the embedded v0.1.9 limit using a
   fixed safe capability detail;
6. delete stale `rowid = -1` before packing/inserting the new probe;
7. insert the probe, query it with `QueryContext` and scan exactly one row,
   close the resulting rows, always attempt delete, then join any query,
   rows-close, and delete root causes through the Task 1 wrapper;
8. preserve `rowID == -1` validation after both operations succeed.

Do not log the DDL, error causes, or database location. Do not call `DROP`,
`DELETE` against rows other than `rowid = -1`, or rebuild any projection.

- [x] **Step 6: Run real lifecycle tests and verify GREEN**

Run the Step 4 command, then:

```text
go test -count=1 ./internal/sqlstore -run '^TestSQLiteVecEmbeddedExtensionAndVec0Schema$'
go test -count=1 ./internal/sqlstore -run '^TestSQLiteVec0ReplaceHydrateAndSearch$'
```

Expected: PASS. Existing normal vec0 schema/search behavior remains intact.

- [x] **Step 7: Add a high-repetition probe cleanup check**

Run the stale-probe test under:

```text
go test -count=25 ./internal/sqlstore -run '^TestSQLiteVecInitializeClearsStaleProbeRow$'
```

Expected: PASS. This proves initialization repeatedly leaves no probe row.

- [x] **Step 8: Commit initialization lifecycle**

```text
git add internal/sqlstore/sqlite_memory_vector.go internal/sqlstore/sqlite_memory_vector_test.go
git commit \
  -m "Fail startup on sqlite vector dimension drift" \
  -m "SQLite vec0 initialization now inspects the owned projection before probing, fails safely on schema/dimension drift, clears stale probe rows, and preserves query and cleanup root causes." \
  -m "Constraint: Never drop or rebuild the projection automatically and never expose DDL, paths, vectors, or root-cause text" \
  -m "Tested: real SQLite mismatch, incompatible-schema, stale-probe, and multi-cause lifecycle tests" \
  -m "Not-tested: public Server startup proof is delivered by the next task" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 3: Prove Application startup failure, map four SQLite cases, and deliver PR2

**Files:**
- Modify: `server/application_test.go`
- Modify: `test/conformance/parity-inventory-rules.json`
- Regenerate: `test/conformance/parity-inventory.json`
- Modify: `AGENTS.md` only if implementation reveals a new reusable rule not already represented.

**Interfaces:**
- Consumes: SQLite index safe mismatch error and initialization lifecycle from Tasks 1-2.
- Produces: public `OpenApplication` startup-failure proof and four case-specific release mappings.

- [x] **Step 1: Add a failing public Application startup test**

Create a file-backed SQLite database and initialize it with embedding profile
dimension `3`. Build a normal `ProcessConfig` with a configured embedding
model/profile dimension `4` and dependencies that provide an embedding model
with profile dimension `4`. Call:

```go
application, err := OpenApplication(t.Context(), config, dependencies)
if application != nil {
    t.Fatal("OpenApplication returned an Application despite projection mismatch")
}
```

Require `errors.As(err, *memory.CapabilityNotSupportedError)`, the safe
dimension/rebuild detail, and absence of DB path/DDL/Memory/vector sentinel
text. After failure, query the original database and prove its owned vec0
schema remains `float[3]` and an authoritative artifact/entry sentinel row
remains present.

- [x] **Step 2: Run startup test and verify RED**

Run:

```text
go test -count=1 -tags sqlite_fts5 ./server -run '^TestOpenApplicationRejectsSQLiteVectorProjectionDimensionMismatch$'
```

Expected: current code returns a generic probe failure or otherwise fails to
provide the stable mismatch contract.

- [x] **Step 3: Run startup test and verify GREEN**

After Task 2 implementation, run the same command. Expected: PASS on a
supported CGO SQLite host. The test must not start an HTTP Server or assert a
manufactured degraded `/health/ready` response.

- [x] **Step 4: Add four exact parity mappings**

Add case-specific rules for:

```text
tests/builtin/persistence/test_sqlite_memory_vector_index.py
  test_vector_index_probe_clears_a_leftover_probe_row
  test_vector_index_probe_reports_a_table_dimension_mismatch
  test_vector_index_probe_surfaces_the_underlying_cause
  test_vector_index_probe_reports_the_provider_limit_for_a_fresh_oversized_dimension
```

Map all four to the exact real SQLite test declarations added in Tasks 1-2.
The public Server test remains cross-layer evidence for startup failure, but
does not substitute for the fresh-oversized SQLite case. Do not map a case to
a generic file-level reason or an injected error that does not exercise the
described boundary.

- [x] **Step 5: Regenerate and inspect parity inventory**

Run:

```text
go run ./tools/parity-inventory-generate -upstream <exact-v0.1.0-checkout>
go run ./tools/parity-inventory-generate -upstream <exact-v0.1.0-checkout> -check
```

Inspect the diff. Exactly the four named SQLite cases move from pending to
mapped. Counts change only through the generator. The provider/readiness six
cases remain mapped.

- [x] **Step 6: Run final changed-surface validation**

Run:

```text
go test -count=1 ./internal/sqlstore -run SQLiteVec
go test -race -count=1 ./internal/sqlstore -run SQLiteVec
go test -count=1 -tags sqlite_fts5 ./server -run 'SQLiteVector|ProjectionDimension|Embedding'
make test-sqlite
make lint
go mod tidy -diff
go mod verify
git diff --check
```

If Windows cannot compile the real CGO tests, document the exact `sqlite3.h`
gap and run parser/wrapper tests using only the repository-supported matching
header include. Do not treat local parser-only success as full index proof.
Linux exact-Head `Standard` and `Main/tests` jobs are required merge evidence.

- [x] **Step 7: Perform test-guard review**

Confirm real SQLite is used for schema and row behavior; fake DBTX is limited
to injecting exact probe query/delete failures while every other operation
delegates to a real database. Ensure no raw SQLite bytes, mock call count, or
source-text assertion substitutes for projection semantics.

- [ ] **Step 8: Refresh Base and create PR2**

Before push:

```text
git fetch --no-prune origin main
git rev-list --left-right --count origin/main...HEAD
git merge-base --is-ancestor origin/main HEAD
```

If Base drifted, rebase and rerun every validation above. Push only after a
fresh Head check. Create:

```text
fix(sqlite): diagnose embedding projection dimension drift
```

PR body must state Part of #3, the startup-failure lifecycle, no automatic
drop/rebuild, safe public versus root-cause matching, exactly four SQLite cases
mapped, and Windows CGO gaps. Reconcile `changedFiles`, REST filenames, and
local Base...Head filenames before using any CI evidence.

- [ ] **Step 9: Wait for exact-Head CI and merge only after all gates**

Require exact Head 37/37 success, zero unresolved review threads, and current
Base `CLEAN` merge state. Verify merge server-side through REST `merged: true`,
then wait for exact-main Main, E2E, CodeQL, License, and Windows workflows.

- [ ] **Step 10: Update Issue only from exact-main evidence**

After post-main success, read main's generated inventory. Update Issue #3
mapping counts and check the combined embedding release-delta item only when
all provider/readiness and SQLite requirements are proven. Keep the wider
all-case mapping item unchecked until all remaining cases resolve.
