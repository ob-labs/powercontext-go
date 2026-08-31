# Conformance tests

This directory makes the Python `v0.0.2` implementation at commit
`3a6cb0151670eaff7dc0293466edd673124e80da` an executable Oracle rather than
an informal reference.

- `parity-contract.json` records the upstream parity identity as separate
  concepts: the upstream repository, the exact target SHA, the signed release
  target and assets, the frozen release Oracle, and the active parity target. `parity_contract_test.go`
  proves the frozen Oracle commit is identical across the contract, the Go
  test constant, `manifest.json`, and the `frozen-oracle` CI checkout, and
  rejects any conflation between the frozen Oracle and the parity targets.
  Update the active parity target only deliberately: confirm the upstream CI
  run at the new commit is green, record the new exact target SHA and its
  test inventory together in one reviewed change, and keep the frozen
  Oracle untouched until a new versioned Oracle directory is accepted.
- `testdata/python-v0.0.2/manifest.json` freezes OpenAPI, SQLite schema,
  Prompt, fixture, and Python-test inventories.
- `traceability.json` inventories every one of the 622 frozen Python test
  functions and records whether its evidence is case-specific or supporting.
  It is generated from `traceability-rules.json`; do not edit the generated
  table by hand.
- `parity-inventory.json` inventories all 812 Python test cases at the
  active parity target recorded in `parity-contract.json`. Every case
  carries a mode (`go-port`, `cross-layer`, or `retained-host`) and a
  status: `mapped` cases have case-specific evidence that resolves against
  real test declarations, `pending` cases await the port that owns their
  mode (for example WP2 for the transport surface). The 622 frozen Oracle
  cases inherit their `traceability.json` mappings verbatim. It is
  generated from `parity-inventory-rules.json` plus an upstream checkout
  pinned at the contract target SHA; do not edit the generated inventory
  by hand, and update the pinned mapped/pending counts only together with
  the rule changes that move cases between the two states.
- `target-delta.json` records the exact 73 added and 20 removed cases between
  the former `f2ec1f75` target and the signed v0.1.0 release. Every removed
  case has a reviewed `removed`, `renamed`, or `superseded` disposition plus
  replacement case or still-resolvable Go/retained-host evidence. The parity
  generator recomputes both case sets from exact Git checkouts and rejects
  ledger, replacement, or evidence drift.
- `authority_test.go` proves Python SQLite → Go read/write → Python back-read.
- `review_database_test.go` proves Python Candidate → Go revise/approve →
  Python Artifact back-read and continued Candidate writes against the same
  SQLite authority database.
- the scheduler suite proves APScheduler → Go rewrite → APScheduler restore.
- the domain and Handoff Report fixtures compare exact constants, errors,
  Memory canonical bytes/hashes (including frozen Python whitespace),
  canonical JSON, and digests.

Regenerate or check the two inventories with:

```sh
go run ./tools/fixture-generate -python ../powercontext
go run ./tools/fixture-generate -python ../powercontext -check
go run ./tools/traceability-generate
go run ./tools/traceability-generate -check
```

Regenerate or check the active parity target inventory and reviewed target
delta against upstream checkouts pinned to the previous and release commits
(the generator verifies both `git rev-parse HEAD` values before extracting):

```sh
go run ./tools/parity-inventory-generate \
  -upstream ../powercontext-v0.1.0
go run ./tools/parity-inventory-generate \
  -upstream ../powercontext-v0.1.0 \
  -check \
  -check-delta \
  -previous-upstream ../powercontext-f2ec1f75 \
  -release-upstream ../powercontext-v0.1.0
```

The frozen-Oracle CI job checks out the exact Oracle, previous target, and
active release target commits; runs the Oracle suite; regenerates the
deterministic fixtures; verifies both generated inventories and the reviewed
target delta; and enables both Python back-read tests. A changed Oracle commit
requires deliberate fixture regeneration and review; changing only a hash to
silence drift is not valid.

The rules carry a monotonically increasing checkpoint for case-specific
evidence. A changed Oracle case must add or update its exact `cases` mapping;
shared file-level coverage is never counted as 1:1 parity.

`testdata/python-v0.0.1` and `python_handoff_report_fixture.py` remain as
read-only historical interoperability evidence. They are not the active
release Oracle.
