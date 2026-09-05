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
understate the new CLI and supported-integration behavior, while no evidence
through this candidate requires a breaking major-line decision.

## Exact-diff record

| Surface | Candidate evidence | Version and operator consequence |
| --- | --- | --- |
| Public CLI | The recorded source-surface analysis for `powercontext-v0.1.0..e4cd0646c553a88a0d99b8422501d0a99c814ffd` adds `powercontext hook workbuddy`. `setup workbuddy` validates a release archive and owns a hook that invokes its `bin/powercontext` binary. No CLI changes occur in `e4cd0646c553a88a0d99b8422501d0a99c814ffd..fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9`. | New observable capability supports a minor release recommendation. Re-run WorkBuddy setup from the extracted archive after upgrade. |
| Public Go API | The recorded source-surface analysis finds no changes under Makefile `PUBLIC_API_PACKAGES`: `api/v1`, `artifact`, `artifact/experience`, `artifact/handoff`, `artifact/memory`, `artifact/skill`, `client`, `inference`, `server`, `source`, or `trigger`. The candidate delta through `fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9` also has no changes there. The `v0.1.0` API baseline is renamed and bound to its release tag. | No public Go API migration is identified. `make api-compat` remains the release gate for incompatible exported changes. |
| OpenAPI | The recorded source-surface analysis leaves `openapi/powercontext.yaml` unchanged, and the candidate delta through `fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9` does not change it. | No HTTP wire-contract migration or regenerated-client change is identified. |
| Server persistence | The recorded source-surface analysis leaves `internal/sqlstore` unchanged, and the candidate delta through `fe12b50a46c4d179a6bb9a2dafe14ba229ebddf9` adds no Server schema or persistence migration. | No database migration is required by this release decision. Preserve the existing SQLite upgrade and projection-safety procedure. |
| Installation and adapters | Codex and WorkBuddy are the only supported integration roots. The WorkBuddy Go hook replaces the installed Python hook boundary; historical adapter source is not redistributed. | The archive must carry the reviewed two-root integration inventory and credential-free configuration boundary. |
| Archive and release process | The candidate adds an explicit integration inventory and release-draft/review flow, separate Standard and Full runtime verification, and final archive evidence checks. | Standard and Full must expose identical Codex and WorkBuddy integrations; only their existing native inference assets differ. Publication remains a separate reviewed action. |

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
