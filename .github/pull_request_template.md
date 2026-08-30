## Tracking

- Tracking issue:
- Relationship: `Part of #...` or `Closes #...`

## Problem and rationale

Describe the verified problem and why this is the smallest safe solution.

## Changes

-

## Behavior and compatibility

- User-visible behavior:
- Public API or protocol impact:
- Breaking changes or migration requirements:
- Explicit non-goals:

## Generated, dependency, and workflow impact

- [ ] No generated contract changed, or every required output was regenerated from its source.
- [ ] No dependency changed, or `go.mod` and `go.sum` are complete and verified.
- [ ] No CI or release contract changed, or stable job names, permissions, timeouts, and failure evidence were reviewed.
- [ ] No credential, private Source/Memory content, or unredacted production log is included.

## Validation

List the exact commands run against the submitted Head and their results.

```text

```

- [ ] `git diff --check`
- [ ] `git status --short` was inspected after validation.
- [ ] Formatter evidence (`make fmt-check` or an explained equivalent) is recorded.
- [ ] Generated-consumer evidence (`make generated-consumers` when generator outputs change, or an explained equivalent) is recorded.
- [ ] Compatibility evidence (`make api-compat` for public Go changes, relevant contract/downstream checks otherwise) is recorded.
- [ ] Relevant generated, race, compatibility, process, adapter, documentation, or release checks were run.
- [ ] Any skipped or unavailable check is identified above and is not represented as passing.

## AI usage

State whether AI assistance was used, what it changed, and how the submitted result was independently reviewed and
verified.
