# Next PowerContext Go release version decision

## Status

This is a recommendation for the next unpublished PowerContext Go release. It
does not create a tag, draft, GitHub Release, archive, image, or publication
claim. This record covers the candidate
`201f49b0fc383d538abc9d2b8d1f78d5d7f3d2fd` against
`powercontext-v0.1.0` at `17a6f000c58ec5801e7341013a42211af97f6d0a`.
That exact range contains 73 commits and 532 changed files. The final tag or PR
Head remains a separate release-time input and must be re-evaluated immediately
before publishing; this document update and the Windows release-inventory
change are intentionally later than the recorded candidate input.

## Recommendation

Recommend **v0.2.0**, subject to the coordinator's final tracker ruling and a
fresh exact previous-tag comparison before release preparation.

The release policy requires a version decision from the exact previous-tag to
candidate comparison and explicitly says that a release must not be assumed to
be a patch release. That policy does not mechanically assign semantic-version
numbers. This recommendation uses the compatible new public functionality in
the candidate range as the material distinction: a patch number would
understate the new CLI and supported-integration behavior, while no evidence
through this candidate requires a breaking major-line decision.

## Exact-diff record

| Surface | Candidate evidence | Version and operator consequence |
| --- | --- | --- |
| Public CLI | The exact range adds canonical Scope, Source, Artifact, managed-Skill, and statistics operations; a released Go WorkBuddy hook; and Windows `server install`, `status`, and `uninstall` lifecycle commands. | These observable additions support a minor release recommendation. Re-run WorkBuddy setup from the extracted archive after upgrade; use native Server lifecycle commands only on supported Windows x64 Standard archives. |
| Public Go API | The exact range changes generated `api/v1` capability types and adds public Source observation/connector/definition/Skill-usage contracts plus managed Skill packaging behavior. | This is compatible-addition work, not an unchanged API surface. `make api-compat` remains the authority for rejecting an incompatible exported change before release. |
| OpenAPI | `openapi/powercontext.yaml` adds required `supported_databases` and `supported_external_agents` fields to the capabilities response, constrained to SQLite, Codex, and WorkBuddy. Canonical sidecar contracts are generated separately and do not hand-edit legacy generated code. | Generated clients and capability consumers must use the candidate contracts; `make check-generated` and the compatibility gates remain required. |
| Server persistence | The exact range adds durable Scope hierarchy/bindings/settings, Connector checkpoints, Definition manifests, accepted-observation markers, managed Skill packages and usage, and related SQLite authority/CAS paths. | This candidate has real SQLite schema evolution. Preserve a backup, use the supported startup upgrade path, and retain the projection-safety procedure; do not describe it as migration-free. |
| Installation and adapters | Codex and WorkBuddy remain the only supported integration roots. The WorkBuddy Go hook replaces the installed Python hook boundary. Windows x64 Standard adds a per-user Task Scheduler service; retained systemd and LaunchAgent contract code does not enter the product matrix. | Archives must carry the reviewed two-root integration inventory and credential-free configuration boundary. Native service support is Windows Task Scheduler only; Linux systemd/user D-Bus/desktop lifecycle and macOS LaunchAgent remain out of scope. |
| Archive and release process | The candidate already has explicit integration inventory, release-draft/review flow, signed provenance, and separate Linux/macOS Standard and Full verification. This change promotes the validated `windows-amd64` Standard archive into the official inventory and adds a published-asset Windows consumer. | Linux/macOS Standard and Full expose identical Codex and WorkBuddy integrations. Windows publishes Standard only, with no Full or native inference assets. Publication remains a separate reviewed action. |

The two supported integrations are the Codex and WorkBuddy command hosts. Both
must consume the extracted archive. WorkBuddy registration must invoke only
the extracted archive binary. These requirements do not make the archive a
credential store: credentials remain runtime environment references and are
not archive content.

## Release prerequisites

Immediately before any future release action, identify the final tag or PR Head
and recompute the exact previous-tag diff through that exact commit. Confirm
the API, OpenAPI, persistence, archive, and adapter evidence above against the
final release-time range, review the generated release draft, and apply the
tracker's publication ruling. A match with this recorded candidate is not a
substitute for that final comparison. The current
`release-shaped-main-evidence-sufficient` ruling is not authority to create a
tag or publish a release.
