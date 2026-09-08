# E4 Upstream Rebaseline Design

## Decision

Refresh the moving-upstream inventory to
`oceanbase/powercontext@e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c` while
keeping the three implemented canonical sidecars pinned to their independently
validated `74b961fbb07165595314726715d412a3d0d90589` source blob.

The current master inventory and a sidecar wire schema have different
lifecycles. Current master is an accountability input: it tells the project
which operations are still deferred. A sidecar source is a public wire
contract: it must not advance until the corresponding runtime behavior exists.
The two must therefore be separately pinned and independently validated.

## Problem

Python upstream now has 94 operations. The existing current-master inventory
has 77 operations from the older upstream snapshot. Replacing the shared raw
sidecar input with e4 would change existing Scope, Source, and Artifact
schemas: access responses, prompt-family acceptance, and tag filters would be
generated without corresponding SQLite/Codex/WorkBuddy product behavior.

Leaving the old inventory untouched is also incorrect: the 17 new operations
would remain invisible to the compatibility ledger and conformance gates.

## Contract Boundaries

- The frozen legacy `openapi/powercontext.yaml`, `api/v1`, legacy Client
  Invoker, and generated MCP tool inventory remain byte-stable.
- `compatibility-surface.json` moves to e4 and records all 55 upstream-only
  operations. The 17 new entries start `deferred`.
- `openapi/canonical/upstream-powercontext.yaml` and the Scope, Source, and
  Artifact manifests remain pinned to 74. Their selected endpoints continue to
  be checked against the e4 inventory by operation ID, method, path, and
  `implemented-canonical` status.
- Sidecar validation must stop asserting that a stable sidecar source has the
  same complete operation set as the moving inventory. It must still reject a
  selected operation that is missing, endpoint-mismatched, or deferred in the
  current inventory. The manifest SHA-256 remains the source-byte authority.
- New e4 operations remain deferred; this change does not add a backend,
  external agent, HTTP route, MCP tool, or persistence behavior.
- Product support remains SQLite only and Codex/WorkBuddy only.

## Data Updates

The e4 inventory updates these current-master facts:

| Field | Old | New |
| --- | --- | --- |
| Upstream commit | `74b961f...` | `e4ebdcd...` |
| Canonical operations | 77 | 94 |
| Upstream-only operations | 38 | 55 |
| Common operations | 39 | 39 |
| Go-only operations | 14 | 14 |

The additional operation clusters are: one Artifact revision-history read,
five Artifact tag endpoints, two Prompt endpoints, and nine Access endpoints.
Each appears in both the compatibility surface and the conformance ledger as
`deferred`. Existing 15 `implemented-canonical` entries retain their status.

## Verification

The change is complete only when:

1. Generator tests prove a 74-pinned selected sidecar source is accepted
   against the e4 inventory, while endpoint/status mismatches are rejected.
2. The exact e4 checkout passes OpenAPI ledger comparison and the master-node
   discovery evidence is regenerated from that checkout, not hand-authored.
3. The frozen release parity inventory remains untouched.
4. `make check-generated`, compatibility/generator tests, conformance
   discovery checks, and the exact-upstream parity gate pass.
5. Generated legacy outputs and existing sidecar outputs have no diff.

## Deferred Consequences

The e4 Artifact revision-history, tag, Prompt, and Access operations do not
become implemented through this rebaseline. A future sidecar must explicitly
pin e4 or a later source and implement all new access/prompt/tag semantics
required by its selected operation before it can be marked
`implemented-canonical`.
