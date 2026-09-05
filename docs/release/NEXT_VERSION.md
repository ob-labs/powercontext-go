# Next PowerContext Go release version decision

## Status

This is a recommendation for the next unpublished PowerContext Go release. It
does not create a tag, draft, GitHub Release, archive, image, or publication
claim. The recommendation was derived from the exact range
`powercontext-v0.1.0..e4cd0646c553a88a0d99b8422501d0a99c814ffd`; it must be
re-evaluated if the release candidate changes.

## Recommendation

Recommend **v0.2.0**, subject to the coordinator's final tracker ruling and a
fresh exact previous-tag comparison before release preparation.

The release policy requires a version decision from the exact previous-tag
comparison and explicitly says that a release must not be assumed to be a
patch release. That policy does not mechanically assign semantic-version
numbers. This recommendation uses the compatible new public functionality in
the range as the material distinction: a patch number would understate the new
CLI and retained-integration behavior, while no evidence in the range requires
a breaking major-line decision.

## Exact-diff record

| Surface | Evidence from `powercontext-v0.1.0..e4cd0646c553a88a0d99b8422501d0a99c814ffd` | Version and operator consequence |
| --- | --- | --- |
| Public CLI | `powercontext hook workbuddy` is added. `setup workbuddy` now validates a release archive and owns a hook that invokes its `bin/powercontext` binary. | New observable capability supports a minor release recommendation. Re-run WorkBuddy setup from the extracted archive after upgrade. |
| Public Go API | No source changes occur under Makefile `PUBLIC_API_PACKAGES`: `api/v1`, `artifact`, `artifact/experience`, `artifact/handoff`, `artifact/memory`, `artifact/skill`, `client`, `inference`, `server`, `source`, or `trigger`. The `v0.1.0` API baseline is renamed and bound to its release tag. | No public Go API migration is identified. `make api-compat` remains the release gate for incompatible exported changes. |
| OpenAPI | `openapi/powercontext.yaml` is unchanged. | No HTTP wire-contract migration or regenerated-client change is identified. |
| Server persistence | `internal/sqlstore` is unchanged; the range adds no Server schema or persistence migration. | No database migration is required by this release decision. Preserve the existing SQLite upgrade and projection-safety procedure. |
| Installation and adapters | The WorkBuddy Go hook replaces the installed Python hook boundary. LangChain is added as a retained Python-package integration, and the retained inventory now records all twelve roots. | The archive must carry the reviewed integration inventory and credential-free configuration boundary. |
| Archive and release process | The range adds an explicit integration inventory and release-draft/review flow, plus separate Standard and Full runtime verification. | Standard and Full must expose identical retained integrations; only their existing native inference assets differ. Publication remains a separate reviewed action. |

The twelve retained integrations are eight command hosts (Claude Code, Codex,
DSH, Hermes, OpenClaw, OpenCode, Pi, and WorkBuddy) and four Python packages
(Bub, LangChain, LangGraph, and Pydantic AI). Command hosts must consume the
extracted archive. Python packages must install and smoke-test from that
archive and their declared lock state. WorkBuddy registration must invoke only
the extracted archive binary. These requirements do not make the archive a
credential store: credentials remain runtime environment references and are
not archive content.

## Release prerequisites

Before any future release action, recompute the exact previous-tag diff for
the candidate commit, confirm the API, OpenAPI, persistence, archive, and
adapter evidence above, review the generated release draft, and apply the
tracker's publication ruling. The current
`release-shaped-main-evidence-sufficient` ruling is not authority to create a
tag or publish a release.
