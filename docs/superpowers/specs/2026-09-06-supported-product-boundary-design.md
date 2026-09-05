# Supported Product Boundary Design

## Goal

Make SQLite, Codex, and WorkBuddy the only supported runtime, CLI, managed
Skill, release, and CI surfaces for the upstream-master alignment tracked by
Issue #202.

## Runtime boundary

`server.ProcessConfig.Validate` rejects every database kind other than
`sqlite` before storage construction. The typed error states the supported
policy without rendering database URLs, filesystem paths, credentials, or the
rejected configured value. `OpenApplication` already validates before calling
`openApplicationStorage`, so this validation is the side-effect boundary.

Agent Skill targets accept `codex` and `workbuddy`. A typed domain error rejects
all other agent kinds before resolving or writing a target path. Codex and
WorkBuddy keep distinct manifest identities while sharing the standard Skill
package shape. Existing Claude Code files remain in the repository as
historical, unsupported source; they do not remain callable product behavior.

## CLI boundary

`setup` and `doctor` retain real Codex and WorkBuddy commands. Previously
published non-target subcommand names remain recognizable but return a typed,
redacted unsupported error before invoking command executors, creating data
directories, or writing host configuration. `setup select` and `doctor
integrations` enumerate only Codex and WorkBuddy as supported choices.

## Release and CI boundary

Standard and Full release archives contain only the Codex and WorkBuddy
integration roots. The source repository may retain other integration
directories, but the release inventory validator distinguishes tracked source
roots from supported redistributed roots. Release evidence, README, install
documentation, and workflow summaries use the same two-agent/SQLite matrix.
Backend-specific and retained-adapter jobs are removed from active workflows.
Their source and historical documentation may remain tracked, but they are not
executed, packaged, or reported as supported Issue #202 evidence.

## Error contract

Unsupported database and agent errors are typed and matchable with
`errors.As`. Their string representations name the supported policy and the
operator action, but never include the rejected URL, token, local path, SQL
parameters, or configured secret. CLI usage errors preserve the typed cause and
exit before any operation runner or filesystem mutation.

## Verification

- Focused Server validation tests prove unsupported databases fail before
  storage selection and SQLite still opens through the public application.
- Agent Skill tests prove Codex and WorkBuddy targets construct and project,
  while every other kind fails before path resolution or writes.
- Real CLI command tests prove unsupported setup/doctor/select inputs execute
  no command or filesystem side effect.
- Release tests inspect extracted Standard and Full archives and require exactly
  the Codex and WorkBuddy integration roots.
- Existing Codex and WorkBuddy service-chain tests remain green with SQLite in
  public and bearer-authenticated modes.

## Non-goals

- Deleting historical source directories for other integrations.
- Implementing Scope, Source, OpenAPI, or remote Skill functionality from later
  Issue #202 phases.
- Supporting seekDB, OceanBase, Claude Code, DSH, Hermes, OpenClaw, OpenCode,
  Pi, LangChain, LangGraph, Pydantic AI, or another host/framework.
