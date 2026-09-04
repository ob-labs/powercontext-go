# WorkBuddy Go Hook Design

## Status

Approved on 2026-09-04. This document defines the independently scoped
WorkBuddy Go-hook slice tracked by Issue #194.

## Context

PowerContext Go is Go-primary, but the installed WorkBuddy
`UserPromptSubmit` hook currently invokes Python. The existing Go CLI already
owns WorkBuddy installation, credential-free persisted configuration,
diagnostics, atomic replacement, and the Server/MCP registration. The Go
Server and SQLite service chain are already the authoritative runtime.

The goal is to replace only the installed WorkBuddy hook driver with the
released Go binary. The observable WorkBuddy contract remains unchanged.

## Scope

In scope:

- Add `powercontext hook workbuddy` to the existing CLI boundary.
- Read one WorkBuddy hook payload from stdin and emit one
  `hookSpecificOutput` JSON object on stdout.
- Read the existing persisted WorkBuddy configuration and retain its
  credential-free authorization-environment reference.
- Resolve agent or project scope, recall prepared context, capture the
  submitted prompt, and optionally flush captured work through the existing
  public Server endpoints.
- Make `setup workbuddy` register a stable absolute path to the released Go
  executable.
- Test the installed-binary path with a release-shaped archive and the real
  SQLite Server/MCP service chain.

Out of scope:

- Migrating Codex, Claude Code, Bub, Hermes, LangChain, LangGraph, Pydantic
  AI, DSH, Pi, OpenCode, OpenClaw, or any other integration.
- A cross-host hook framework or a new public Go package.
- seekDB, OceanBase, and non-SQLite runtime requirements.
- Persisting bearer tokens, prompts, source content, database locations, or
  configured URLs in diagnostics.
- Forking, creating, or maintaining an external consumer repository.

## Options Considered

1. A WorkBuddy-only Go subcommand in `internal/cli` is selected. It keeps
   installation, persisted configuration, process I/O, and host-specific
   behavior at the existing CLI boundary, while the Server remains the only
   persistence owner.
2. A Go wrapper that starts the existing Python driver is rejected. It keeps
   the Python runtime dependency and does not establish a release-binary
   contract.
3. A generic multi-host hook framework is rejected. Existing hosts have
   different payload, installation, and lifecycle contracts; adding an
   abstraction before a second Go hook increases scope without removing
   present complexity.

## Architecture

### Command boundary

`newHookCommand` gains only a `workbuddy` child. The child owns stdin/stdout,
runtime environment reads, persisted configuration lookup, and fail-open
process behavior. It does not expose a new library API.

The command accepts no positional arguments. It reads a bounded UTF-8 JSON
object from stdin. Only a `UserPromptSubmit` event is processed; other valid
events return successfully without output. A processed event writes exactly:

```json
{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"..."}}
```

`additionalContext` is an empty string when no context is available. The
command writes no prompt, authorization value, configured URL, scope ID, or
Server body to stdout or stderr.

### Configuration and scope

The hook reads the existing `WORKBUDDY_HOME/powercontext.json` schema through
the same internal configuration decoder used by setup and diagnostics. An
invalid or missing persisted configuration is authoritative fail-open input:
the command performs no recall or capture and returns successfully.

The persisted Server URL, authorization-environment name, scope mode, request
timeout/budget, and byte limits remain authoritative. Environment variables
may supply only the already-defined runtime-owned scope, capture, and flush
controls. A Go implementation replaces the installed Python scope resolver;
the agent/project scope results must match the current WorkBuddy contract.

### Transport and operation flow

The hook uses a CLI-owned HTTP client that enforces the same transport policy
at client construction and final request dispatch. Loopback requests bypass
ambient proxies. Remote plaintext endpoints are rejected before a request is
sent; remote HTTPS retains the configured proxy behavior.

For one valid prompt, the operation is:

1. Resolve scope from `cwd` and persisted/runtime scope controls.
2. Use one wall-clock request budget for all Server calls.
3. POST `/v1/context/prepare` with bounded query and configured `max_bytes`.
4. Validate the public prepared-context response and emit its content, or an
   empty context for empty, unavailable, invalid, timeout, or authentication
   outcomes.
5. When capture is enabled and the prompt fits the configured source limit,
   derive the existing deterministic WorkBuddy source ID, POST
   `/v1/sources/content`, and optionally call `/v1/memory/flush` until the
   returned cursor reaches the captured source position or the bounded flush
   limit is exhausted.

All hook failures are fail-open. They are represented by empty context and a
bounded, redacted stderr event only when the existing contract permits an
event. No failure changes the Hook exit code to nonzero.

### Installation and release archive contract

`setup workbuddy` resolves the executable that is running setup through
`os.Executable`, resolves symlinks, and validates that it is a regular,
executable `powercontext` binary beneath a supported released archive root.
It writes the resolved, shell-quoted absolute path followed by
`hook workbuddy` into the WorkBuddy `UserPromptSubmit` command registration.

The setup command must reject an unresolved checkout-local, `go run`,
temporary, or PATH-only binary rather than silently registering one. Existing
settings, MCP, configuration, ownership, and rollback rules remain in force.
The installed Python Hook driver and Python scope resolver are no longer
required runtime artifacts for a successful Go-hook installation; retained
documentation and unrelated integration assets are not removed in this slice.

## Error and Security Rules

- One malformed payload, invalid configuration, unsupported event, scope
  failure, transport refusal, Server error, oversized response, or timeout
  must not leak protected inputs or block WorkBuddy.
- The authorization environment supplies the value only at invocation time;
  setup and persisted configuration retain the variable name, never its value.
- A request body and response are bounded before decoding or logging.
- Authentication, loopback bypass, public mode, remote HTTPS, and trusted
  caller-supplied transport behavior use the established host transport
  policy; this slice does not invent a parallel policy.

## Verification

Focused tests must prove:

- Payload parsing, event filtering, exact stdout JSON, empty context, scope
  modes, capture/flush, deterministic source IDs, request budgets, malformed
  inputs, and fail-open behavior.
- Persisted configuration is authoritative; invalid persisted configuration
  prevents legacy environment fallback.
- Public and bearer-authenticated Server operation, loopback proxy bypass,
  remote plaintext refusal, and redaction of prompts and credentials.
- `setup workbuddy` records the released binary path, not Python, `go run`, a
  temporary build, the current directory, or an incidental PATH binary.
- A release-shaped archive installs the hook and invokes its own binary.
- A real SQLite Server service chain captures a prompt, recalls it through the
  Hook, and exposes it through the configured MCP `search_memory` route in
  both public and bearer-authenticated modes.

The targeted CLI, release, pre-WP6 host-adapter, and service-chain gates run
before a PR is considered ready. The exact PR Head and post-merge main must
then be green before the WorkBuddy checkbox in Issue #194 is updated.

## Acceptance Boundary

Completion replaces only WorkBuddy's installed Python Hook driver. It does
not mean that Python assets have been removed from the repository, and it does
not reopen completed Codex/WorkBuddy compatibility evidence or make any claim
about other retained adapters.
