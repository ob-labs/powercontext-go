# v0.1.0 public Go API baseline

`v0.1.0.apidiff` is the path-independent export-data bundle for the eleven
deliberate public packages named in ADR 0002. `make api-compat` resolves the
`v0.1.0` tag to commit `17a6f000c58ec5801e7341013a42211af97f6d0a` and verifies
that this bundle exactly matches the historical bundle recorded in that tag
before comparing the current package exports. The bundle is generated with the
Go version selected by `go.mod`, the repository's pinned `x/tools` writer, and
the pinned `golang.org/x/exp/cmd/apidiff` version in the root Makefile.

Run `make api-compat` to reject removed or incompatibly changed exported
identifiers. Compatible additions are allowed. Run `make api-baseline` only
for an approved compatibility change. Capture the incompatible report before
updating the bundle, then review that report together with the proposal,
migration notes, versioning decision, and regenerated package inventory.

This baseline is the compatibility boundary for the current v0.1 release line.
For a later release line, add a separately named baseline and bind it to that
tag's exact commit; do not infer the baseline from a branch name.
