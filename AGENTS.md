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
- When authoritative state changes successfully before best-effort discovery or
  bookkeeping runs, preserve the successful response if that follow-up fails.
  Emit one bounded warning per degraded call, and verify the public response
  reflects authoritative state without logging raw errors, identifiers, or paths.
- When a generator rewrites generated code to call project-specific support,
  emit that support as a declared generated artifact and verify a fresh
  temporary module can tidy, verify, and test the complete output.
- When a Go package has build-tagged implementation variants, keep its package
  documentation in an untagged `doc.go` and give accompanying C, C++, or
  assembly sources the same constraints as the Go implementation that owns
  them. Verify `Doc`, `GoFiles`, `CgoFiles`, and native source fields with
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
- When an end-to-end readiness probe owns a child process, race in-flight
  readiness against child completion, retain the most recent bounded log tail,
  include the exit code or signal, and prove the failure path with a real
  short-lived child whose diagnostic follows over-limit output.
- When a scheduled operation is traced outside a request, create one explicit
  transport-owned root and finish it from the scheduler's aggregate outcome.
  Keep every runtime stage beneath that root, make cancellation dominate
  failure/success/noop, and verify both trace-parent isolation and that scope
  IDs or protected inputs never become exported attributes.
- When a test invokes a freshly built external binary, disable result caching
  or key it to the binary content. Verify the gate reruns after the binary
  changes without changing the test package.
- When a test synchronously invokes real CLI or compiler subprocesses, measure
  cold supported-host durations and set a bounded timeout on that test only;
  do not raise the suite-wide default. Verify the real exit status and output,
  then prove the focused test completes on the measured slow host.
- When a release or end-to-end CLI adds a required flag, inventory every direct
  invocation instead of updating only wrapper targets. Verify each script and
  Make entrypoint with executable fakes that assert the complete argument list.
- When a harness recursively removes a temporary home, create the exact
  deletion target itself under any caller-supplied parent. Verify a parent
  sentinel survives failed startup cleanup.
- When a consumer constructs generated nullable request fields, represent a
  required JSON `null` explicitly instead of relying on a zero value, and add
  a focused request-validation test before exercising the transport.
- When an external consumer acknowledges a Handoff, use a fresh receipt source
  ID that is distinct from the work boundary and has not already been captured;
  verify the accepted acknowledgement through the public HTTP client.
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
- When a fixture generator writes byte-frozen text artifacts, request UTF-8
  output with an explicit LF newline and verify both raw Windows output and
  Linux regeneration against the committed digest; checkout attributes alone
  cannot repair a manifest that hashed pre-normalized CRLF bytes.
- When Make recipes enable Bash nounset or pipefail globally, expand optional
  environment variables in preflight guards with explicit defaults and run
  real failed-pipeline and missing-variable probes on every supported Make
  runtime; source-text checks alone do not prove the execution contract.
- When an always-run CI cleanliness step must diagnose tracked and untracked
  side effects, capture one porcelain status before deciding success; never put
  a fail-fast dirty-tree command ahead of the diagnostic. Print only a bounded
  prefix plus the omitted-path count. Verify both the parsed workflow contract
  and real clean, staged, unstaged, untracked, and over-limit repositories.
- When a governance parser exempts a GitHub Actions reusable-workflow caller
  from ordinary-job requirements, require a nonblank `uses` reference and
  reject every keyword outside GitHub's supported caller-job set. Verify the
  rule with fixtures for a valid caller, a blank reference, `timeout-minutes`,
  and ordinary-only fields such as `runs-on` or `steps`.
- When a repository gate reads a versioned structured contract or mirrors an
  external schema, reject duplicate and unknown fields, validate compound
  identifiers by their exact segment shape, and check executable workflow
  steps by the complete required operation rather than substring presence.
  Keep a valid fixture plus a failing mutant for each boundary, and recheck the
  assumptions against the current official schema before changing the gate.
- When enabling a syntax-only linter rule for a method or selector, inspect the
  pinned analyzer implementation and prove it with a repository-shaped mutant
  using the receiver and call forms present in production. Do not advertise a
  rule whose matcher ordinary identifier or selector naming can bypass.
- When a repository inventory prunes generated output while discovering owned
  files, scope name-based exclusions to explicit repository-relative roots
  unless that directory class is unowned at every depth. Verify an owned nested
  path with the same basename is still discovered.
- When a repository inventory accepts repository-relative paths, reject the
  exact parent path as well as parent-prefixed normalized paths before joining
  or comparison. Verify the exact parent boundary reports an escape.
- When blank Issues are disabled, inventory every supported request class and
  provide a distinct validated form or contact route for each. Verify that the
  chooser routes bounded features separately from compatibility-sensitive
  contract or platform proposals.
- When `go install module@version` provisions a repository-local tool, run the
  install with the project-selected `go env GOVERSION` as the minimum
  `GOTOOLCHAIN`. Verify from an older bootstrap Go release that the installed
  binary can process the module's declared Go version.
- When a repository writes `gcexportdata` bundles for an `apidiff` baseline,
  use an empty `token.FileSet`, reject absolute machine paths and package-list
  drift, and pin an `apidiff` build that uses the same `x/tools` export format
  as the writer. Verify deterministic regeneration on Windows and Linux and
  prove that removing a real exported identifier fails the public API gate.
- When a dependency audit inspects owned Go module graphs, use readonly package
  resolution and verify `go.mod` and `go.sum` remain unchanged; do not download
  graph metadata in a way that expands a checked-in module manifest.
- When a CI Makefile target iterates over a platform, package, or owned-module matrix, verify
  a successful fake command observes every required entry and intended
  environment. Make a non-final entry fail, then verify the target exits
  nonzero without running later entries.
- When remediating a transitive dependency vulnerability, declare the
  resolution in the affected package manager's root manifest rather than
  editing only the lockfile. Regenerate with the package-manager version pinned
  in CI, preserve required license headers, and verify both a frozen install
  and an authoritative audit eliminate every vulnerable dependency path.
- When a locked dependency manifest records public registry or distribution
  URLs, do not make CI depend on a third-party mirror that can reject a clean
  runner. Regenerate with the resolver version pinned in CI against the
  official registry while preserving resolved package versions and hashes, then
  verify the locked install and reject mirror URLs with a focused contract test.
- When a Go release artifact is stripped, do not treat a binary scanner's
  module-only fallback as symbol-level evidence. Build an unstripped scan twin
  from the same entrypoint, tags, dependency lock, and version metadata, and
  verify build-before-scan ordering and exact arguments with executable fakes.
- When a test must cancel while a synchronous foreign-function call is in
  flight, coordinate entry and release with explicit barriers instead of a
  fixed sleep. Verify the public error and cleanup side effects under high
  repetition on every supported architecture.
- When an embedding transport success fixture accepts a requested dimension,
  return vectors with at least that dimension instead of relying on later
  padding or validation gaps. Verify the fixture under every supported
  full-build-tag platform matrix.
- When an opt-in real-provider smoke accepts an arbitrary embedding model,
  require one explicit positive model-supported dimension from the same smoke
  configuration and require the returned vector length to match it exactly.
  Verify the configuration path with a non-default positive dimension and
  missing, non-numeric, and non-positive mutants without calling a provider.
- When provider request shape depends on a model ID, use one exact normalized
  classifier for both accepting the model and selecting every shape-specific
  field. Verify near-miss versions and same-provider non-target models are
  rejected before the provider client runs.
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
- When a Client supports per-request endpoint overrides or caller-supplied HTTP
  clients, enforce the plaintext loopback policy both at construction and in
  the final RoundTripper. Verify an unsafe override fails before the underlying
  transport runs, and allow an HTTP-labelled non-loopback route only when the
  caller supplies the client and explicitly vouches for transport security.
  Return a typed refusal that preserves configuration-error matching, names the
  non-loopback policy, and never includes the rejected URL in its representations.
- When a cross-field security policy or parent command depends on optional CLI
  overrides, preserve explicit presence separately from the value, reject an
  explicit blank instead of treating it as omission, and merge or forward valid
  overrides before final validation. For environment merges, verify both safe
  and unsafe override directions; for parent forwarding, verify omission keeps
  the child default while an explicit blank reaches child validation before any
  write.
- When a CLI converts a typed configuration failure into a usage error, keep
  the underlying error matchable and name concrete operator remediation without
  exposing configured values. Verify exit code 2 and that execution never
  reaches the operation runner.
- When a bytecode parser retains stack markers across later stack mutations,
  validate every stored marker against the current stack length before slicing
  or indexing. Verify stale-marker tuple and map operations return the typed
  parse error, then run the bounded decoder fuzz target without a panic.
- When a CLI option consumes the following token as a value, reject a missing
  value or another option before any read or write. Verify the real CLI exits
  nonzero with an actionable diagnostic even when an option-named file would
  otherwise make the malformed command succeed.
- When isolated host adapters vendor the same transport policy or construct
  proxy-aware HTTP clients, drive every configuration and directly constructible
  client boundary from one shared host-vector fixture. Bypass environment and OS
  proxies for loopback routes before bearer credentials can leave the host while
  preserving configured proxies for non-loopback routes. Verify the complete IPv4
  `127.0.0.0/8` range, IPv6 loopback, remote plaintext rejection, and each explicit
  trusted-transport exception, and prove proxy controls after clearing inherited
  proxy and no-proxy variables case-insensitively.
- When setup owns a credential-free authorization environment reference, treat
  every syntactically valid generated `${NAME:-}` reference as owned rather
  than only the default name. Verify repeated setup with a custom authorization
  environment preserves the no-token persistence contract.
- When an installed host adapter has a persisted credential-free configuration
  file, treat an invalid existing file as authoritative failure-open input
  instead of falling back to legacy environment configuration. Verify through
  the host hook entrypoint that invalid persisted configuration emits no
  recalled context.
- When a documentation contract test extracts executable shell commands, stop
  parsing at a shell comment before deciding that a command exists. Verify a
  fully commented command cannot satisfy executable-documentation evidence and
  that a valid command still reaches the real CLI boundary.
- When a workflow contract test injects a step by replacing the next-job
  boundary, anchor the mutation to the target job's actual adjacent boundary
  after structural changes. Verify the mutant changes that target job and is
  rejected by the contract validator.
- When CLI behavior depends on whether stdin is interactive, use an actual
  terminal query instead of treating every character device as a TTY. Verify
  null devices and pipes remain non-interactive while a controlled terminal
  input still enables the prompt path.
- When a CI verifier accepts endpoint overrides and may attach credentials,
  require HTTPS before request construction. Verify a plaintext override fails
  before the RoundTripper runs and that neither the credential nor remote body
  appears in the returned error.
- When setup returns a host-home path after resolving symlinks, compare tests
  against the same resolved path rather than the raw input path. Verify a
  symlinked temporary-root path and an ordinary path so platform aliases such
  as macOS `/var` cannot cause a false setup failure.
- When a CLI reads a shell-style environment file, parse assignments without
  evaluating shell expansion, recognize managed markers only as complete
  lines, and redact credential-bearing URLs, headers, and cookies. Verify
  quoted and escaped literals, rejected expansion and control input, invalid
  UTF-8, marker text inside values, repeated markers, and both human and JSON
  output.
- When a host autoloads a plugin from a global directory, publish its constant
  ownership manifest before atomically replacing the bundle and treat an
  existing unowned bundle as a conflict. Verify an interrupted first install
  leaves only recoverable ownership state, a retry repairs it, and a bounded
  no-model activation lifecycle covers configured, active, inactive, exited,
  timeout, and cleanup outcomes through real child-process boundaries. In a
  packaged release-consumer test, prove the installed bundle bytes and manifest
  originate from the release archive rather than asserting a retired installer
  command.
- When a pinned native extension imposes a fixed schema dimension limit,
  validate the configured dimension against that exact versioned limit before
  creating any projection DDL. Verify a fresh over-limit profile returns a
  typed, redacted error that states both limits, creates no projection, and
  leaves the extension's raw diagnostic unexposed.
