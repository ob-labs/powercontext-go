# WorkBuddy Go Hook Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace only the installed WorkBuddy Python `UserPromptSubmit` driver with the released PowerContext Go binary while preserving WorkBuddy's observable hook, configuration, security, and SQLite service-chain contracts.

**Architecture:** Keep Host process I/O, configuration, scope resolution, and HTTP orchestration in `internal/cli`. Add one `powercontext hook workbuddy` child and a narrow runtime value object in the same package; it uses the generated public Go client for `/v1/context/prepare`, `/v1/sources/content`, and `/v1/memory/flush`. `setup workbuddy` resolves the running release binary and persists its shell-quoted absolute path, instead of installing a Python driver.

**Tech Stack:** Go 1.27, Cobra, generated `api/v1` and `client` packages, `internal/transportpolicy`, SQLite server, MCP Go SDK, Go test, release archive test harness.

**Spec:** `docs/superpowers/specs/2026-09-03-workbuddy-go-hook-design.md`

## Global Constraints

- Modify no generated `api/v1` or `client` files; `openapi/powercontext.yaml` remains the HTTP source of truth.
- Keep the Go-hook implementation under `internal/cli`; add no top-level package, cross-Host hook framework, seekDB, OceanBase, or external consumer work.
- Read endpoint, authorization-environment reference, scope mode, timeouts, budgets, and byte limits from persisted `WORKBUDDY_HOME/powercontext.json`; only existing runtime-owned scope/capture/flush controls may come from environment.
- Treat missing or invalid persisted configuration as fail-open: emit no recalled context, capture nothing, and exit zero.
- Never persist or render prompts, source content, bearer values, raw scope IDs, configured URLs, database locations, request bodies, or response bodies.
- Use the existing loopback/remote plaintext transport policy and preserve public plus bearer-authenticated operation.
- `setup workbuddy` accepts only a resolved regular release binary under `<archive-root>/bin/powercontext`; reject checkout-local, `go run`, temporary, and PATH-only targets.
- Keep the WorkBuddy Host timeout at 10 seconds; individual hook calls share the persisted bounded request budget.
- Keep all code comments and maintained documentation in English.

---

### Task 1: Add the WorkBuddy Hook Command and Protocol Tests

**Files:**
- Create: `internal/cli/workbuddy_hook.go`
- Create: `internal/cli/workbuddy_hook_test.go`
- Modify: `internal/cli/root.go:130-158`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `workBuddyConfiguration`, `readWorkBuddyConfiguration`, `workBuddyHome`, and `transportpolicy.IsPlaintextNonLoopback` from the existing CLI boundary.
- Produces: `newHookCommand(state *commandState) *cobra.Command` and `runWorkBuddyHook(ctx context.Context, input io.Reader, output, diagnostics io.Writer, runtime workBuddyHookRuntime) error`.
- Produces: a Task 1 `workBuddyHookRuntime` with `configuration workBuddyConfiguration`. Task 2 extends that private value with injected environment, HTTP client, and clock dependencies when those values first affect behavior.

- [ ] **Step 1: Write failing command-discovery and stdout-contract tests**

Add tests that construct `newCommandWithAllDependencies`, find `hook workbuddy`, supply one `UserPromptSubmit` JSON payload, and assert exact compact JSON output:

```go
want := `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":""}}\n`
if got := stdout.String(); got != want {
	t.Fatalf("hook stdout = %q, want %q", got, want)
}
```

Cover non-`UserPromptSubmit` events, malformed/oversized stdin, missing configuration, invalid configuration, a blank prompt, and a missing `cwd`. Also set invalid non-empty `POWERCONTEXT_CLIENT_SERVER_URL` and invalid `POWERCONTEXT_CLIENT_TIMEOUT` while persisted WorkBuddy configuration is valid; the Hook must still exit successfully with the exact response and no diagnostics. Each case must disclose neither the prompt nor the authorization value. Recall-generated content is owned by Task 2, not this protocol-boundary task.

- [ ] **Step 2: Run the focused test before implementation**

Run: `go test -count=1 ./internal/cli -run 'TestWorkBuddyHook(Command|FailOpen)'`

Expected: FAIL because `hook` is absent and `runWorkBuddyHook` does not exist.

- [ ] **Step 3: Register the command and implement bounded payload handling**

Add `newHookCommand` to the root command list. In `workbuddy_hook.go`, define bounded input and output constants, strict UTF-8 JSON decoding with duplicate/unknown-field rejection, and these private types:

```go
type workBuddyHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"`
	UserPrompt    string `json:"user_prompt"`
	CWD           string `json:"cwd"`
	SessionID     string `json:"session_id"`
	PromptID      string `json:"prompt_id"`
	RequestID     string `json:"request_id"`
}

type workBuddyHookResponse struct {
	HookSpecificOutput struct {
		HookEventName    string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}
```

For every handled failure, write only the compact empty-context response and return `nil`. Read persisted configuration before any legacy environment fallback. Do not emit output for an unrelated event.

- [ ] **Step 4: Run protocol tests and the command inventory test**

Run: `go test -count=1 ./internal/cli -run 'Test(WorkBuddyHook|SetupAndDoctorExposeCurrentHostMatrix)'`

Expected: PASS; the root has `hook workbuddy`, invalid inputs fail open, and exact JSON is stable.

- [ ] **Step 5: Commit the protocol boundary**

```bash
git add internal/cli/root.go internal/cli/workbuddy_hook.go internal/cli/workbuddy_hook_test.go internal/cli/root_test.go
git commit -m "feat(workbuddy): add fail-open Go hook command"
```

### Task 2: Implement WorkBuddy Scope, Client Operations, and Security Regressions

**Files:**
- Modify: `internal/cli/workbuddy_hook.go`
- Modify: `internal/cli/workbuddy_hook_test.go`
- Test: `client/client_test.go` only if an existing generated-client behavior lacks a required regression; do not edit generated source.

**Interfaces:**
- Consumes: `pcclient.New`, `pcclient.Client.PrepareContext`, `CaptureContentSource`, and `FlushMemory`; `v1.PrepareContextRequest`, `v1.CaptureContentSourceRequest`, and `v1.FlushMemoryRequest`.
- Produces: private `resolveWorkBuddyHookScope`, `recallWorkBuddyContext`, `captureWorkBuddyPrompt`, and `flushWorkBuddyPrompt` helpers, each taking `context.Context` first.
- Produces: deterministic source IDs `workbuddy-user-prompt:<sha256(scope + "\\x00" + session + "\\x00" + promptID + "\\x00" + prompt)>`.

- [ ] **Step 1: Write failing operation tests through an HTTP test server**

In `workbuddy_hook_test.go`, use an `httptest.Server` and assert the exact ordered public calls:

```go
wantPaths := []string{"/v1/context/prepare", "/v1/sources/content", "/v1/memory/flush"}
if diff := cmp.Diff(wantPaths, gotPaths); diff != "" {
	t.Fatalf("request paths (-want +got):\n%s", diff)
}
```

Assert request scope, configured `max_bytes`, deterministic `source_id`, optional flush behavior, one shared deadline, and empty context for 401, 404, 503, invalid prepared-context payload, too-large response, and timeout. Add a transport probe proving 127.0.0.0/8 and IPv6 loopback bypass a proxy while remote HTTP is refused before the test transport runs.

- [ ] **Step 2: Run the operation tests before implementation**

Run: `go test -count=1 ./internal/cli -run 'TestWorkBuddyHook(RecallCaptureFlush|Transport|Redaction)'`

Expected: FAIL because the Go hook has no scope/client operation flow.

- [ ] **Step 3: Implement scope and generated-client operation flow**

Implement project scope in the CLI package with the existing observable rules: explicit runtime scope, `workbuddy:agent`, Git-private `powercontext/codex-workspace.json`, normalized `remote.origin.url`, then a SHA-256 local-path fallback. Bound emitted scope values to 256 bytes exactly as the retained WorkBuddy resolver does.

Construct `pcclient.New` using the persisted server URL, runtime authorization value from the persisted environment-name field, the lesser of persisted request timeout and remaining shared budget, and a loopback-aware HTTP client. Convert all client/server/validation errors to the empty-context fail-open response without copying their text to output.

Use the generated request/response types. Validate `PreparedContext` schema/status/content before use; capture only a nonblank prompt that fits `SourceMaxBytes`; stop flushing when the returned cursor reaches the capture position or the runtime-owned `POWERCONTEXT_WORKBUDDY_FLUSH_MAX_CALLS` limit.

- [ ] **Step 4: Run focused behavior and race checks**

Run: `go test -count=1 -race ./internal/cli -run 'TestWorkBuddyHook(RecallCaptureFlush|Transport|Redaction)'`

Expected: PASS with no proxy credential, prompt, authorization, configured URL, or scope value in stdout/stderr/assertion failures.

- [ ] **Step 5: Commit the operation flow**

```bash
git add internal/cli/workbuddy_hook.go internal/cli/workbuddy_hook_test.go
git commit -m "feat(workbuddy): route hook through Go server client"
```

### Task 3: Make Setup Register a Released Binary and Migrate Existing Hook Ownership

**Files:**
- Modify: `internal/cli/integration_workbuddy.go:39-90,223-315,399-416,760-845`
- Modify: `internal/cli/integration_workbuddy_test.go`
- Modify: `integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json`
- Modify: `integrations/workbuddy/README.md`

**Interfaces:**
- Consumes: `os.Executable`, `filepath.EvalSymlinks`, `workBuddyConfiguration`, existing snapshot/rollback helpers, and `shellQuoteWorkBuddy`.
- Produces: `resolveWorkBuddyReleaseBinary() (string, error)` and `workBuddyHookCommand(binary string) string`.
- Produces: setup registration `'<absolute-binary>' hook workbuddy`, with the existing 10-second WorkBuddy timeout and existing custom hook fields preserved.

- [ ] **Step 1: Write failing setup and diagnostics tests**

Add tests that create a release-shaped root containing `bin/powercontext`, `BUILD-INFO.json`, `.env.example`, `openapi/powercontext.yaml`, and the WorkBuddy integration. Assert setup writes the resolved binary path plus `hook workbuddy`, preserves a foreign sibling hook and custom fields, migrates an owned Python command, and never copies `workbuddy_powercontext_hook.py`, `workbuddy_settings.py`, `prepared_context.py`, or `powercontext_project_scope.py` into `WORKBUDDY_HOME`.

Add negative tests for executable paths outside a release root, a missing/nonregular binary, a `go-build` temporary path, and a legacy hook whose binary ownership cannot be established. Each negative case must leave settings, MCP, configuration, hook directory, and skill unchanged.

- [ ] **Step 2: Run setup tests before implementation**

Run: `go test -count=1 ./internal/cli -run 'Test(SetupWorkBuddy.*(Release|Binary|Hook)|DoctorWorkBuddy)'`

Expected: FAIL because setup still writes `python3 ...workbuddy_powercontext_hook.py` and installs Python files.

- [ ] **Step 3: Implement release-binary resolution and atomic migration**

Resolve and evaluate symlinks for `os.Executable`. Require a regular file named `powercontext` under `bin/`, and require its parent archive root to contain `BUILD-INFO.json`, `.env.example`, `openapi/powercontext.yaml`, and `integrations/workbuddy/plugins/powercontext`. Return a redacted operator-facing error if any invariant fails.

Pass the resolved binary through `installWorkBuddyPlugin` before snapshots are mutated. Change `mergeWorkBuddySettings` and `isWorkBuddyHook` to recognize both the legacy Python command and the owned `hook workbuddy` command, then replace only owned entries with the shell-quoted Go command. Remove Python hook installation from `installWorkBuddyHooks`; update diagnostics to validate the registered Go command rather than the retired script files. Update the static template and README to describe the Go binary contract without exposing a real binary path or token.

- [ ] **Step 4: Run setup, diagnostic, and documentation checks**

Run: `go test -count=1 ./internal/cli -run 'Test(SetupWorkBuddy|DoctorWorkBuddy|BundledWorkBuddy)'`

Run: `make docs-test`

Expected: PASS; an installed WorkBuddy registration calls only the release binary and the credential-free persisted schema remains unchanged.

- [ ] **Step 5: Commit release-binary installation**

```bash
git add internal/cli/integration_workbuddy.go internal/cli/integration_workbuddy_test.go integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json integrations/workbuddy/README.md
git commit -m "feat(workbuddy): install released Go hook binary"
```

### Task 4: Prove the Archive Consumer and Real SQLite/MCP Service Chain

**Files:**
- Modify: `test/e2e/workbuddy_service_chain_test.go`
- Modify: `tools/release/main_test.go:424-570`
- Modify: `.github/workflows/migration-gates.yml` only if a current required host-adapter command cannot invoke the new Go service-chain test; preserve separate Codex and WorkBuddy evidence steps.
- Modify: `tools/release/workflow_test.go` only if the workflow contract changes.

**Interfaces:**
- Consumes: `server.OpenApplication`, generated WorkBuddy release archive fixture, `cli.New`, and MCP `search_memory`.
- Produces: an installed-binary service-chain proof that invokes `<release-root>/bin/powercontext hook workbuddy` for public and bearer-authenticated Server modes.
- Produces: a release-consumer proof that rejects a hook command outside the unpacked release root.

- [ ] **Step 1: Rewrite the existing Python service-chain test as a failing Go-binary test**

Replace the Python interpreter and installed `.py` driver invocation in `TestWorkBuddyHookAndMCPShareOneGoServiceConfiguration` with a subprocess of the built Go binary and `hook workbuddy`. Keep the existing two-mode table, real SQLite application, first-prompt capture, second-prompt recall, and MCP `search_memory` assertion. Add an assertion that the installed `settings.json` command names the extracted archive's binary and does not contain `python`, `.py`, a checkout root, or a bearer token.

- [ ] **Step 2: Run the service-chain test before implementation**

Run: `go test -count=1 -tags sqlite_fts5 ./test/e2e -run TestWorkBuddyHookAndMCPShareOneGoServiceConfiguration`

Expected: FAIL until setup and the Go hook are implemented because the existing installed command still needs Python.

- [ ] **Step 3: Extend the release consumer test**

In `TestReleaseArchiveProvidesConsumableAdapterSources`, include the WorkBuddy archive entry and run `<release-root>/bin/powercontext setup workbuddy --source <release-root>`. Assert `settings.json` points at the extracted binary, invoke the command with a valid Hook payload, and prove that replacing the registered command with a path outside the extracted root is rejected by diagnostics or setup without mutation.

- [ ] **Step 4: Run the integration and release-focused gates**

Run: `go test -count=1 ./tools/release -run 'Test(ReleaseArchiveProvidesConsumableAdapterSources|StageIntegrationsIncludesEveryRuntimeAdapter)'`

Run: `go test -count=1 -tags sqlite_fts5 ./test/e2e -run TestWorkBuddyHookAndMCPShareOneGoServiceConfiguration`

Run: `go test -count=1 ./internal/cli`

Expected: PASS for public and bearer-authenticated capture/recall/MCP paths and release-owned binary registration.

- [ ] **Step 5: Commit the end-to-end proof**

```bash
git add test/e2e/workbuddy_service_chain_test.go tools/release/main_test.go .github/workflows/migration-gates.yml tools/release/workflow_test.go
git commit -m "test(workbuddy): prove released Go hook service chain"
```

### Task 5: Final Validation, Tracker Evidence, and Pull Request

**Files:**
- Modify: `docs/release/INSTALL.md` only if the release archive installation section still describes a Python WorkBuddy runtime.
- Modify: `https://github.com/ob-labs/powercontext-go/issues/194` only after merged and post-main evidence exists.

**Interfaces:**
- Consumes: the completed Hook, setup, archive, e2e, and workflow contracts.
- Produces: exact-Head and post-main evidence for the WorkBuddy Go-hook checkbox; it does not claim other Host migrations or a Python-free repository.

- [ ] **Step 1: Verify generated and formatting boundaries before broad validation**

Run: `make check-generated`

Run: `gofmt -d internal/cli/workbuddy_hook.go internal/cli/workbuddy_hook_test.go internal/cli/integration_workbuddy.go internal/cli/integration_workbuddy_test.go`

Expected: no generated diff and no Go formatting output.

- [ ] **Step 2: Run the changed-surface quality gates**

Run: `go test -count=1 -race ./internal/cli`

Run: `go test -count=1 ./tools/release`

Run: `go test -count=1 -tags sqlite_fts5 ./test/e2e -run TestWorkBuddyHookAndMCPShareOneGoServiceConfiguration`

Run: `make lint`

Expected: all pass; report any missing Windows SQLite header or unsupported local native prerequisite as an environment gap rather than weakening the assertions.

- [ ] **Step 3: Reconcile the final diff and create the implementation PR**

Before opening the PR, verify that every changed file belongs to the five planned boundaries and that no generated `api/v1` or `client` file changed. The PR body must reference #194 and #159, state that the slice migrates only WorkBuddy, and include the exact targeted commands and their results.

- [ ] **Step 4: Confirm exact-Head and post-merge evidence**

Require the PR's exact Head `Main`, Pre-WP6 host adapters, Post-WP6 retained host adapters, release-contract/archive, E2E, Windows, CodeQL, and License Check workflows to complete. After squash merge, refetch the exact `main` SHA and record each post-main conclusion before changing Issue #194.

- [ ] **Step 5: Update tracking only after evidence is complete**

Mark the Independent v0.1.x WorkBuddy Go-hook slice in #194 complete only when the exact implementation Head and the merged `main` both pass the relevant release, adapter, and service-chain evidence. Update/close #159 only with a readback that says the delivered scope is WorkBuddy-only and that no multi-Host migration was delivered.
