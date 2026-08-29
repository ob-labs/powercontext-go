# Conformance tests

This directory makes the Python `v0.0.2` implementation at commit
`3a6cb0151670eaff7dc0293466edd673124e80da` an executable Oracle rather than
an informal reference.

- `parity-contract.json` records the upstream parity identity as four
  separate concepts: the upstream repository, the exact target SHA, the
  frozen release Oracle, and the active parity target. `parity_contract_test.go`
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

The frozen-Oracle CI job independently checks out the exact Python commit,
runs its suite, regenerates the deterministic fixtures, and enables both
Python back-read tests. A changed Oracle commit requires deliberate fixture
regeneration and review; changing only a hash to silence drift is not valid.

The rules carry a monotonically increasing checkpoint for case-specific
evidence. A changed Oracle case must add or update its exact `cases` mapping;
shared file-level coverage is never counted as 1:1 parity.

`testdata/python-v0.0.1` and `python_handoff_report_fixture.py` remain as
read-only historical interoperability evidence. They are not the active
release Oracle.
