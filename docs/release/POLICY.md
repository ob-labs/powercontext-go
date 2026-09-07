# PowerContext Go Release Policy

PowerContext Go publishes from reviewed repository state. The default branch receives features and fixes. Supported
release branches receive approved fixes and security backports only. In short, the default branch receives features and
fixes, while supported release branches receive approved fixes and security backports only.

## Branches

`main` is the default integration branch. The release branch naming convention is `release/vX.Y`, where `X.Y` is the
supported minor release line. Default and release branches must be protected from force-push and deletion in GitHub
branch rules before a release line is advertised as supported.

Pull requests to a supported release branch must be backport PRs. A backport PR must identify the original Issue and
change, the target release line, conflict resolution, compatibility impact, and validation performed on that line.
Feature work belongs on `main`; release branches receive compatibility-preserving fixes and security backports.

## Merge Strategy

PowerContext Go uses squash merge for ordinary pull requests so each reviewed change has one release-note and
provenance unit. A release-line exception must be explained in the backport PR when preserving original commit identity
is part of the validation evidence.

## Release Notes

Release notes are generated from reviewed labels and then edited before publication. The `.github/release.yml`
categories are the release-note contract:

- `breaking`: breaking changes
- `enhancement`: features
- `bug`: bug fixes
- `security`: security fixes
- `dependencies`: dependency updates
- `documentation`: documentation updates
- `maintenance`: maintenance, CI, tooling, and release work

Compatibility-affecting pull requests need an explicit breaking-change label and version decision. Do not infer that
every release is a patch release. A release draft must be generated with an exact previous-tag comparison and
contributor attribution, then reviewed before publication. Tag pushes and a normal `release` workflow dispatch only
create or refresh that draft. A maintainer publishes it only through a separate `release` workflow dispatch with
`publish_release` set to true after review; publication then unlocks release verification and documentation deployment.

## Version Sources

Root-module, CLI, generator, adapter, and binary versions must be verified from their authoritative source and tag.
Do not assume every tool shares the root-module version. Release verification must resolve the tag to the released
commit, exercise the published binary and image surfaces, and include migration and backport notes for
compatibility-affecting fixes.

## Signed Build Provenance

Every downloadable release artifact and immutable artifact digest must have signed build provenance created by the job
that produced its final bytes or digest. The binary producer jobs sign archives, SPDX documents, and platform checksum
manifests. The image producer signs the exact standard and Full OCI digests while retaining the BuildKit SBOM and
maximum-provenance attestations. The draft producer signs the final aggregate checksum and image-digest records before
it creates or updates the GitHub Release.

Tag pushes already run on the released tag. A manual draft refresh or publication must also dispatch `release.yml` from
the exact tag named by `release_tag`; the workflow rejects a branch ref or a different tag before building. Consumers
must verify the attestation before execution or extraction, bind verification to `ob-labs/powercontext-go` and
`.github/workflows/release.yml`, and then validate checksums, embedded build metadata, SPDX evidence, and runtime
behavior. A checksum without the signed repository and workflow identity is not release provenance.

## DCO

DCO sign-off is not required for PowerContext Go pull requests. If the project later adopts DCO, documentation and CI
must add the enforcement gate in the same reviewed change.
