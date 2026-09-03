# Backend release audit ledger

This ledger is the Phase 5 evidence boundary for Issue #194. It reconciles the
v0.1.0 persistence cases before the final seekDB and OceanBase implementation
phases. It is an audit, not a declaration that either optional backend has
release parity.

## Inputs and rules

- Alignment target: `powercontext-v0.1.0` at
  `7b736206a53a6de6f43d4b517893ee1a80e7183d`.
- Case inventory: `test/conformance/parity-inventory.json`, generated from the
  pinned target. At the audit revision it contains 46 cases below
  `tests/builtin/persistence`, all with case-specific mapping evidence.
- Machine-readable case ledger:
  `test/conformance/backend-audit-ledger.json`. Its conformance test rejects a
  missing, duplicate, unknown, or unclassified persistence case before either
  final backend can consume the shared contract.
- Authority rule: immutable Artifact revisions, Memory manifests and entry
  versions, and Source journal state are authoritative. FTS and vector tables
  are rebuildable projections; a migration or rebuild must not rewrite an
  authoritative revision.
- Identity rule: `scope_id`, `source_id`, and other relational identity values
  remain case-sensitive. MySQL-mode DDL uses `utf8mb4_bin` for identity
  columns; SQLite and seekDB work must retain the same observable distinction.
- Error rule: backend failure diagnostics must remain typed and bounded. They
  must not render SQL bind values, Memory content, credentials, database URLs,
  or database locations.

The ledger deliberately separates an existing Go mapping from release evidence
for a final backend. A mapped Python case can be current SQLite proof, a shared
semantic requirement, or backend-specific implementation evidence. Only the
last category may close the corresponding final backend phase.

## Case ledger

| Upstream file and cases | Classification | Current evidence | Required later evidence |
| --- | --- | --- | --- |
| `test_artifacts.py` (4): ordered lineage, stale revision CAS, concurrent initial creation, lineage/head foreign keys | Common authoritative-storage behavior, already covered by current SQLite | `internal/sqlstore/artifacts_test.go` and `artifact_create_conflict_test.go` | Reuse the same public/store contract for both final backends; verify restart and schema-upgrade behavior against real backend instances. |
| `test_cursors.py` (2): generation CAS and safe initial creation | Common authoritative-storage behavior, already covered by current SQLite | `internal/sqlstore/review_test.go` | Run the identical CAS and concurrent-create contract through seekDB and OceanBase; no backend may substitute last-write-wins behavior. |
| `test_experience_index.py` (4): MySQL-compilable schema, SQLite legacy upgrade, OceanBase `MEDIUMTEXT` upgrade, SQLite FTS rebuild | Split: SQLite current proof plus OceanBase-specific migration/index proof | `experience_index_migration_test.go`, `experience_index_test.go`, `database_oceanbase_test.go` | OceanBase finalization must exercise the `MEDIUMTEXT` upgrade on a disposable live tenant. seekDB finalization needs its own restart/rebuild proof, not a copied MySQL DDL assertion. |
| `test_external_skills.py` (2): provider/host replacement and scope isolation | Already covered by current SQLite; backend-agnostic projection semantics | `product_surfaces_test.go` | No standalone backend feature is created. Include scope isolation in each backend's shared regression matrix when that backend persists external skills. |
| `test_memory.py` (4): MySQL schema limits, authority plus FTS atomicity, head/FTS rebuild, scope isolation | Common Memory semantics; SQLite proof exists, with one OceanBase schema prerequisite | `memory_test.go`, `sqlite_memory_fts_test.go`, `database_oceanbase_test.go` | Shared tests must prove authority survives projection rebuild. OceanBase and seekDB must each prove FTS/restart behavior by public Server and Client paths. |
| `test_mysql_schema.py` (3): binary identity collation, canonical `MEDIUMBLOB`, InnoDB key budget | OceanBase-specific relational-dialect prerequisite | `database_oceanbase_test.go` | Retain static DDL checks and add a real OceanBase migration/identity test with case-only `scope_id` and `source_id` values. |
| `test_oceanbase_profile.py` (6): official URL, redaction, dialect, SQL parameter privacy, MySQL tenant, live smoke | OceanBase-specific | `database_oceanbase_test.go`, `server/config_test.go`, `test/e2e/oceanbase_live_test.go` | Extend the live tenant gate from schema/cursor/report smoke to identity, restart, FTS/vector search, projection rebuild, and redacted failure behavior. |
| `test_provider.py` (4): source identity conflicts, artifact/cursor transaction, concurrent capture/flush, inference outside transaction | Common runtime-to-store behavior, already covered by current SQLite | `sources_test.go`, `memory_flush_test.go`, `http_memory_test.go` | Run shared store-contract tests for both backends. Public integration evidence must prove inference runs outside SQL transactions and an Artifact/Cursor update commits atomically. |
| `test_seekdb_profile.py` (6): path, local socket, close order, repeated-cancellation cleanup | seekDB-specific | `internal/sqlstore/seekdb/seekdb_test.go`, `database_seekdb_test.go`, `server/seekdb_test.go` | Final seekDB work must add supported Linux/macOS embedded startup, restart persistence, public FTS/vector/hybrid search, and typed unsupported-Windows evidence. |
| `test_sources.py` (4): shared source journal, idempotent source identity, scope boundary, concurrent allocator | Common authoritative-storage behavior, already covered by current SQLite | `sources_test.go` | Each backend receives the same source-idempotence, case-sensitive identity, and concurrent journal-allocation suite. |
| `test_sqlite_memory_vector_index.py` (4): stale probe cleanup, dimension mismatch, root-cause matching, dimension limit | SQLite-only current-backend proof | `sqlite_memory_vector_test.go`, `sqlite_memory_vector_schema_test.go`, `server/application_test.go` | Do not copy sqlite-vec probe mechanics. Reuse only the common profile/dimension, safe-error, projection-rebuild, and authority-preservation contracts. |
| `test_sqlite_profile.py` (3): SQLite dialect/path and foreign-key pragmas | SQLite-only current-backend proof | `server/config_test.go`, `database_test.go`, `artifacts_test.go` | No seekDB/OceanBase implementation task arises from SQLite path or pragma details. |

## Required shared contract matrix

The final backend PRs must consume a small shared test contract rather than
reimplementing the following semantics independently:

1. Case-sensitive `scope_id` and `source_id` values remain distinct across
   create, conflict, lookup, restart, and schema-upgrade paths.
2. Artifact revisions, Memory revisions, Source journal entries, and Cursor
   compare-and-swap updates are authoritative and transactional. Projection
   rebuilds may repopulate indexes only from that authority.
3. An embedding profile is the exact tuple of model, profile ID, dimension,
   and normalization. A backend validates its concrete projection against that
   tuple before writing a projection, and reports profile/dimension drift with
   a typed, redacted error.
4. Search paths preserve FTS/vector/hybrid behavior and isolate scope. A
   backend-specific index may be unsupported, but it must report that state as
   typed unsupported or not executed, never as a successful skipped test.
5. SQL and native failures retain programmatic error matching while their
   public rendering excludes bound parameters, source or Memory content,
   credentials, and database paths or URLs.

## Delivery boundaries

The next backend-related PRs must remain in this order:

1. A shared-contract PR, only if the current test suite cannot express one of
   the five matrix rows without backend-specific branching. It may add shared
   test helpers and fixtures, but must not add seekDB or OceanBase runtime
   behavior.
2. The final seekDB PR or PR stack: Linux/macOS embedded lifecycle, restart,
   search, projection/rebuild, native asset and license evidence, plus the
   deliberate Windows unsupported contract.
3. The final OceanBase PR or PR stack: live MySQL-mode migration, binary
   identity, restart, search, projection/rebuild, redacted failures, driver
   and dialect provenance, licenses/SBOM, and release archive evidence.

Before either final backend PR is marked complete, rerun the generated parity
inventory, identify the exact PR Head, confirm the exact-Head backend jobs,
confirm the merge on `main`, and record post-main conclusions. This ledger
does not update Issue #3 or #194 by itself.
