# Next PowerContext Go release version decision

## Status

This is a recommendation for the next unpublished PowerContext Go release. It
does not create a tag, draft, GitHub Release, archive, image, or publication
claim. This record covers the candidate
`fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9` against
`powercontext-v0.1.0`. The source-surface analysis below was recorded at
`e4cd0646c553a88a0d99b8422501d0a99c814ffd`; the candidate delta through
`fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9` was also checked. The final tag or
PR Head remains a separate release-time input and must be re-evaluated
immediately before publishing.

## Recommendation

Recommend **v0.2.0**, subject to the coordinator's final tracker ruling and a
fresh exact previous-tag comparison before release preparation.

The release policy requires a version decision from the exact previous-tag to
candidate comparison and explicitly says that a release must not be assumed to
be a patch release. That policy does not mechanically assign semantic-version
numbers. This recommendation uses the compatible new public functionality in
the candidate range as the material distinction: a patch number would
understate the new CLI and retained-integration behavior, while no evidence
through this candidate requires a breaking major-line decision.

## Exact-diff record

| Surface | Candidate evidence | Version and operator consequence |
| --- | --- | --- |
| Public CLI | The recorded source-surface analysis for `powercontext-v0.1.0..e4cd0646c553a88a0d99b8422501d0a99c814ffd` adds `powercontext hook workbuddy`. `setup workbuddy` validates a release archive and owns a hook that invokes its `bin/powercontext` binary. No CLI changes occur in `e4cd0646c553a88a0d99b8422501d0a99c814ffd..f1bf135b52e2c2ae3ab92f47a4128f8db2ebf0d2`. | New observable capability supports a minor release recommendation. Re-run WorkBuddy setup from the extracted archive after upgrade. |
| Public Go API | The recorded source-surface analysis finds no changes under Makefile `PUBLIC_API_PACKAGES`: `api/v1`, `artifact`, `artifact/experience`, `artifact/handoff`, `artifact/memory`, `artifact/skill`, `client`, `inference`, `server`, `source`, or `trigger`. The candidate delta through `f1bf135b52e2c2ae3ab92f47a4128f8db2ebf0d2` also has no changes there. The `v0.1.0` API baseline is renamed and bound to its release tag. | No public Go API migration is identified. `make api-compat` remains the release gate for incompatible exported changes. |
| OpenAPI | The recorded source-surface analysis leaves `openapi/powercontext.yaml` unchanged, and the candidate delta through `f1bf135b52e2c2ae3ab92f47a4128f8db2ebf0d2` does not change it. | No HTTP wire-contract migration or regenerated-client change is identified. |
| Server persistence | The recorded source-surface analysis leaves `internal/sqlstore` unchanged, and the candidate delta through `f1bf135b52e2c2ae3ab92f47a4128f8db2ebf0d2` adds no Server schema or persistence migration. | No database migration is required by this release decision. Preserve the existing SQLite upgrade and projection-safety procedure. |
| Installation and adapters | The WorkBuddy Go hook replaces the installed Python hook boundary. LangChain is added as a retained Python-package integration, and the retained inventory records all twelve roots. The candidate delta completes archive staging, command-host and Python-consumer proof, integration evidence, release-workflow verification, and the corresponding release documentation. | The archive must carry the reviewed integration inventory and credential-free configuration boundary. |
| Archive and release process | The candidate adds an explicit integration inventory and release-draft/review flow, separate Standard and Full runtime verification, and the final archive-consumer and evidence checks. | Standard and Full must expose identical retained integrations; only their existing native inference assets differ. Publication remains a separate reviewed action. |

The twelve retained integrations are eight command hosts (Claude Code, Codex,
DSH, Hermes, OpenClaw, OpenCode, Pi, and WorkBuddy) and four Python packages
(Bub, LangChain, LangGraph, and Pydantic AI). Command hosts must consume the
extracted archive. Python packages must install and smoke-test from that
archive and their declared lock state. WorkBuddy registration must invoke only
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
