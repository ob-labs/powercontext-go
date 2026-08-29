# PowerContext Go Engineering Rules

## Research and evidence before execution

- When a task depends on external or version-sensitive facts, perform a bounded
  network search for the facts that can affect the decision, even when the
  answer seems familiar. Do not rely only on model memory.
- Start with primary, official sources: Go documentation and release notes,
  upstream package documentation and repositories, GitHub Actions
  documentation and action repositories, protocol specifications, and vendor
  security advisories. Use secondary sources only to find or cross-check the
  primary evidence.
- Verify version-sensitive facts at the time of the task, including supported
  Go versions, command and flag behavior, dependency APIs, action inputs,
  operating-system support, and current issue, PR, or CI state. Search-result
  snippets and remembered URLs are not verification.
- Inspect the current repository, tests, generated contracts, configuration,
  logs, and working-tree state as well. Official documentation describes the
  upstream contract; the checked-out code and reproducible commands establish
  how this repository currently behaves.
- Keep the search proportional to the task and connect each source to a claim
  or risk being checked. Do not collect unrelated links or use research as a
  substitute for implementation and validation.
- Never put secrets, credentials, private source, customer data, or sensitive
  local paths into a search query. If network access or an authoritative source
  is unavailable, state what could not be verified and do not present the
  unverified point as current fact.

## Architectural commitments

- Preserve observable compatibility with the authoritative OpenAPI contract,
  current domain behavior, persistence formats, and concurrency semantics.
- `openapi/powercontext.yaml` is the HTTP source of truth. Files generated into
  `api/v1` must never be edited by hand.
- Core packages do not read environment variables, open databases, start
  goroutines, or own process lifecycle.
- `internal/runtime` owns scope validation, operation lifecycle, per-scope write
  serialization, scheduler coordination, and use-case orchestration.
- Inference must run outside SQL transactions. Artifact or Candidate writes and
  their associated cursor CAS must commit atomically.
- SQL projections and indexes are rebuildable; immutable Artifact revisions and
  Memory manifests remain authoritative.

## Dependency direction

```text
source / artifact / trigger / inference
  -> artifact families
  -> internal/review / internal/contextpack / internal/stats
  -> internal/work / internal/handoffreport
  -> internal/runtime
  -> internal/endpoint
  -> internal/httpapi / internal/mcpapi / internal/webui
  -> server / cmd
```

- `internal/sqlstore` may import public and internal domain packages but must
  not import `internal/runtime`, `server`, or transport packages.
- Domain packages must not import SQL, HTTP, MCP, environment configuration, or
  provider-specific implementations.
- Interfaces belong to their consumers unless they are deliberate public Core
  extension contracts.
- A new top-level Go package must define a deliberate public extension or
  product contract. Product-only domain and orchestration code belongs under
  `internal`; directory creation is not a substitute for cohesive same-package
  files.
- Do not introduce `pkg`, `src`, `common`, `utils`, `helpers`, or global
  `models`, `services`, and `repositories` packages.

## Mandatory Modern Go skill

- At the start of every agent session in this repository, load and follow the
  installed `use-modern-go` skill from
  `https://github.com/JetBrains/go-modern-guidelines` before performing
  repository work.
- Before analyzing, reviewing, writing, modifying, fixing, or refactoring Go
  code, run the skill's `list` command for each relevant Go file or the
  repository's resolved Go version, and read the complete unfiltered output.
  Follow every applicable returned guideline.
- Use the skill's `explain` command only for specific guideline IDs that need
  further evaluation. If the skill is unavailable, cannot be loaded, or its
  wrapper fails, stop before changing Go code and report the blocker instead
  of relying on remembered guidance.

## Go conventions

- Put `context.Context` first on every operation that can block or perform I/O.
- Prefer explicit constructors and narrow interfaces. Do not use a DI framework.
- Keep domain errors typed and map them to wire errors only in
  `internal/endpoint`.
- Use `log/slog` through `internal/observability/logging`; do not create a
  parallel logging interface.
- Log an error once at the operation-owning boundary. Never log Source or
  Memory content, queries, prompts, vectors, credentials, raw scope IDs, full
  database URLs, or unredacted local paths.
- Keep unit tests beside packages. Put cross-backend, differential, and process
  tests under `test`.

## Bug fixes and regression proof

- Establish the failure before changing the implementation. Reduce it to the
  smallest reproducible command, test, request, fixture, or log evidence that
  still demonstrates the externally observable problem and its cause.
- Prefer adding or selecting a regression test before the fix. Demonstrate
  that the test fails against the faulty behavior, then apply the smallest
  repair and demonstrate that the same test passes. Preserve the failure and
  success output needed to explain why the change is effective.
- Test through a public entry point when the defect crosses a package or
  process boundary. Run a targeted unit or package test first, then the
  relevant integration, process-smoke, differential, or end-to-end test when
  configuration, persistence, generated contracts, transports, host adapters,
  concurrency, or lifecycle behavior is involved.
- Match validation to the affected risk. Use repository gates such as
  `make check-generated`, `make test-race`, `make test-sqlite`, `make smoke`,
  `make test-full`, or focused `go test` commands when they directly answer a
  changed-path risk. Do not run expensive unrelated checks merely for ceremony.
- Inspect command output, not only exit codes. Do not make a failing test pass
  by weakening assertions, deleting coverage, increasing timeouts without
  evidence, or changing the required behavior to match the bug.
- If the original failure cannot be reproduced deterministically, document the
  observed evidence, attempted reproductions, environmental limits, and the
  alternative verification used. Do not claim the root cause or fix is proven
  more strongly than the evidence supports.
- A bug fix is not complete until the targeted regression proof passes after
  the change and any required end-to-end or public entry point verification has
  completed, or the remaining validation gap and risk are explicitly reported.

## Learned bug-prevention rules

- After every bug fix, add or strengthen one concise rule in this section that
  would prevent the same class of defect or make it fail earlier. The rule is
  part of the fix, not an optional follow-up.
- Write the rule as a reusable trigger, required action, and verification
  method. Prefer invariant-oriented guidance over incident history, filenames,
  commit IDs, dates, or descriptions of one test failure.
- Merge with an existing rule when the lesson is already covered. Keep the
  stronger wording and remove duplication so this file remains a practical
  engineering contract rather than an append-only incident log.
- When a Go package has build-tagged implementation variants, keep its package
  documentation in an untagged `doc.go`. Verify the package `Doc` field with
  `go list -e` under every supported `GOOS` and CGO selection.
- When a query checks `Rows.Err` before returning, its deferred cleanup must
  merge only `Rows.Close`; do not call a helper that reads `Rows.Err` again.
  Verify injected iteration and close failures remain matchable while each
  root error appears only once.
- When a CI end-to-end test starts a compiled Go service under a readiness
  deadline, build the binary once before the deadline and pass its absolute
  path to every test worker. Verify the workflow with empty `GOMODCACHE` and
  `GOCACHE` directories so dependency download and compilation cannot hide in
  the service startup budget.
- When an end-to-end readiness probe owns a child process, race readiness
  against child completion, include its exit code or signal and a bounded log
  buffer in startup failures, and prove the failure path with a real
  short-lived child process.
- When a consumer constructs generated nullable request fields, represent a
  required JSON `null` explicitly instead of relying on a zero value, and add
  a focused request-validation test before exercising the transport.
- When an external consumer acknowledges a Handoff, capture a distinct
  receiver Source and use its source ID for the receipt; verify the accepted
  acknowledgement through the public HTTP client rather than reusing a work
  boundary source.
- Compare regenerated SQLite fixtures through schema and row semantics, not
  raw database bytes that contain the producing SQLite version and physical
  page-layout metadata. Keep committed fixture hashes pinned separately, and
  verify both semantic regeneration and cross-runtime read/write compatibility.
- When defining or changing LF checkout rules, enumerate every tracked
  byte-sensitive executable, module manifest or checksum, prompt, schema,
  dataset, fixture, and generated-contract format, including extensionless
  files. Verify representative paths with `git check-attr eol` and raw
  working-tree bytes in a fresh `core.autocrlf=true` checkout so clean-filter
  normalization cannot hide CRLF.
- When Make recipes enable Bash nounset or pipefail globally, expand optional
  environment variables in preflight guards with explicit defaults and run
  real failed-pipeline and missing-variable probes on every supported Make
  runtime; source-text checks alone do not prove the execution contract.
- When a governance parser exempts a GitHub Actions reusable-workflow caller
  from ordinary-job requirements, require a nonblank `uses` reference and
  reject every keyword outside GitHub's supported caller-job set. Verify the
  rule with fixtures for a valid caller, a blank reference, `timeout-minutes`,
  and ordinary-only fields such as `runs-on` or `steps`.
- When a repository gate mirrors an external structured schema, validate every
  supported variant's required attributes and keep a valid fixture plus a
  failing mutant for each variant boundary. Recheck the assumptions against
  the current official schema before changing the validator.
- When blank Issues are disabled, inventory every supported request class and
  provide a distinct validated form or contact route for each. Verify that the
  chooser routes bounded features separately from compatibility-sensitive
  contract or platform proposals.
- When `go install module@version` provisions a repository-local tool, run the
  install with the project-selected `go env GOVERSION` as the minimum
  `GOTOOLCHAIN`. Verify from an older bootstrap Go release that the installed
  binary can process the module's declared Go version.
- When a CI Makefile target iterates over a platform or package matrix, verify
  a successful fake command observes every required entry and intended
  environment. Make a non-final entry fail, then verify the target exits
  nonzero without running later entries.
- When remediating a transitive dependency vulnerability, declare the
  resolution in the affected package manager's root manifest rather than
  editing only the lockfile. Regenerate with the package-manager version pinned
  in CI, preserve required license headers, and verify both a frozen install
  and an authoritative audit eliminate every vulnerable dependency path.
- When a test must cancel while a synchronous foreign-function call is in
  flight, coordinate entry and release with explicit barriers instead of a
  fixed sleep. Verify the public error and cleanup side effects under high
  repetition on every supported architecture.
- When a stacked pull request's prerequisite lands or its base branch is
  rewritten, rebuild the branch on the current intended base with only the
  pull request's own semantic changes. Verify the base is an ancestor of the
  new Head and reconcile GitHub, REST, and local diff file counts before using
  prior tests, comments, or CI as evidence.
- When integrating branches changes or removes a Go test path, remove helpers
  that no remaining test calls. Verify the affected packages with both their
  focused tests and the pinned lint policy so dead integration residue cannot
  survive a green test run.
- When a Go function retains an outer error value, do not redeclare `err` in a
  nested short declaration. Use operation-specific names and verify the change
  with the pinned `make lint` policy, including `govet` shadow analysis.
