# Pre-release public Go API baseline

`pre-release.apidiff` is a path-independent export-data bundle for the eleven
deliberate public packages named in ADR 0002. It is generated with the Go
version selected by `go.mod`, the repository's pinned `x/tools` writer, and the
pinned `golang.org/x/exp/cmd/apidiff` version in the root Makefile.

Run `make api-compat` to reject removed or incompatibly changed exported
identifiers. Compatible additions are allowed. Run `make api-baseline` only
for an approved compatibility change. Capture the incompatible report before
updating the bundle, then review that report together with the proposal,
migration notes, versioning decision, and regenerated package inventory.

The baseline is a pre-release change-control mechanism. It does not declare Go
v1 compatibility before the repository publishes its first supported release.
