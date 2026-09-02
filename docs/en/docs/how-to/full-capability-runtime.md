# Full-capability Memory Verification

This guide verifies the existing Go Server Source-to-Memory path. It does not
require a new API or a provider-specific integration. Run the Server with the
configuration that owns the SQLite database and the Memory extraction profile:

```sh
./bin/powercontext server run
```

Use one stable Scope ID throughout this guide. The capture, flush, entry list,
and search requests must use the same value.

```sh
SCOPE_ID=project:quickstart
SOURCE_ID="quickstart-$(date +%s)-$$"
```

## Capture a Source

Capture a durable fact with a unique Source ID. The response is accepted when
it has HTTP status 202, `status` equal to `accepted`, and a numeric `position`.

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/sources/content \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\",\"source_id\":\"${SOURCE_ID}\",\"content\":\"Prefer small, verifiable PowerContext changes.\"}"
```

Keep the response `position`; it is the source journal boundary for the next
step.

## Flush and Inspect Memory

Flush the Scope to process pending Sources into Memory.

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/flush \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\"}"
```

The response contains `current_cursor`. It must reach the capture `position`.
An `idle` response is valid only when its `current_cursor` has already reached
that position; otherwise run the flush again.

List the current Memory entries:

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/entries/list \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\"}"
```

Find the entry whose `source_refs` includes `${SOURCE_ID}`. Record that
entry's `citation.entry_id`. This is the evidence that the captured Source
became an authoritative Memory entry.

## Search the Recorded Entry

Search the same Scope and verify the recorded citation is returned:

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/search \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\",\"query\":\"verifiable PowerContext changes\",\"mode\":\"auto\",\"limit\":10}"
```

A successful round trip has a hit with the recorded `citation.entry_id` and a
non-empty `matched_by` array. The selected search mode depends on the active
SQLite capabilities: FTS is available without an embedding model, while vector
and hybrid modes require the configured embedding projection.
