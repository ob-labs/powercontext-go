# Contributing to PowerContext Go

PowerContext Go accepts focused fixes, compatibility work, documentation, and quality improvements that preserve the
repository's observable contracts. Start from the current repository state and keep each pull request limited to one
reviewable concern.

## Support and compatibility policy

- Go 1.27.0 is the minimum supported toolchain. The `go` directive in `go.mod` is authoritative.
- Compatibility jobs use `GOTOOLCHAIN=local`; an automatic toolchain download must not hide a minimum-version failure.
- Raising the Go version requires a tracking issue and coordinated updates to `go.mod`, CI, Docker and release builders,
  the README, and this document.
- `main` is the current default integration branch. Derive a pull request's base from the live repository policy instead
  of assuming a historical development branch.
- The frozen Python v0.0.2 Oracle under `test/conformance/testdata/python-v0.0.2` is immutable. A newer compatibility
  target must use a separate versioned baseline.
- The deliberate public Go packages are checked against `test/api-compat`. Run `make api-compat` before submitting a
  public-package change. Regenerate with `make api-baseline` only when the linked proposal explains every incompatible
  change, migration requirement, and versioning decision.

## Before starting work

1. Search open issues and pull requests for the same scope.
2. Check whether someone is already assigned or has publicly claimed the work.
3. Open a contract or platform proposal before implementing changes to public APIs, persistence formats, wire behavior,
   lifecycle or concurrency ownership, or supported Go toolchains and platforms.
4. Link substantial changes to a tracking issue before implementation. Narrow bug fixes and maintenance work do not
   need a separate proposal when an existing issue already captures the scope.
5. Keep repairs, tooling, generators, governance, and feature behavior in separate pull requests.

Do not include credentials, access tokens, private source content, or unredacted production logs in an issue, commit,
test fixture, or pull request. Report security vulnerabilities through the repository security policy rather than a
public issue.

## Development environment

Use the repository Make targets so local and CI execution share the same pinned tools and commands:

```sh
make build-all
make lint
make check
make contract-test
make unit-test
make e2e-test
```

`make lint` installs the pinned golangci-lint release under `.tools/bin`. Its embedded gofmt, gofumpt, and goimports
versions are the formatting authority; no mutable global linter installation is required.

Run `make coverage` when a change affects executable Go behavior. It uses race-enabled atomic coverage and rejects a
material regression from the recorded baseline.

## Code and generated contracts

- Match the existing package boundaries, names, and error model. Avoid unrelated refactors or speculative extension
  points.
- Keep public packages deliberate. New public APIs require an observable consumer need and compatibility tests.
- `openapi/powercontext.yaml` is the HTTP source of truth. Do not hand-edit generated files under `api/v1`, the generated
  Client invoker, MCP schemas, or retained adapter operation tables.
- After changing a generator input, run `make check-generated` and commit every required generated output.
- Preserve repository-local tool pinning and do not replace exact versions or Action SHAs with `latest`, a branch, or a
  mutable tag.

## Tests and validation

Choose checks that prove the changed contract, then run the repository gates that cover the affected surface:

- Go implementation or test changes: `go test ./...`, `make test-race`, `make lint`, and `make check`.
- OpenAPI or generator changes: `make contract-test` and `make check-generated`.
- Host adapter changes: the adapter's install, build, generated-diff, unit, surface, and real call-through tests.
- Documentation changes: `make docs-test`.
- License-header changes: `make license-check`.
- Release or native-asset changes: the relevant standard and Full build, package, and smoke targets.

Before committing, inspect the actual output of:

```sh
git diff --check
git status --short
```

If a required check cannot run because of credentials, native assets, or an external service, state exactly what was
not executed and why. A skipped suite or an all-skipped compatibility path is not a passing result.

## Commits and pull requests

Use a concise Conventional Commit-style subject such as `fix: handle cleanup errors` or
`ci: add readonly build gate`.

Every implementation pull request must:

- link its tracking issue and say whether it closes the issue or is only part of it;
- explain the problem, rationale, observable behavior, API or compatibility impact, and explicit non-goals;
- list the exact validation commands that were run against the submitted Head;
- identify generated, dependency, workflow, security, or user-facing changes;
- disclose material AI assistance and describe the human verification performed;
- avoid weakening assertions, broad exclusions, hidden skips, or unrelated cleanup to make a gate pass.

After every pushed change, recheck the exact pull request Head, current comments, and current CI conclusions before
claiming the work is ready.
