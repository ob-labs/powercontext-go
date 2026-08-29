# Security Policy

## Supported Versions

PowerContext Go is currently a pre-release project and has not published any
tagged releases. Security fixes are provided only for the current `main`
development line.

| Development line | Supported |
| --- | --- |
| Current `main` | Yes |
| Earlier commits, snapshots, and downstream forks | No |

This policy will be updated when the project publishes its first supported
release. Reports should identify the commit that was tested. When it is safe
and practical, also state whether the issue reproduces on the latest `main`,
but do not delay a report or perform unsafe verification solely to do so.

## Reporting a Vulnerability

Report suspected vulnerabilities through the repository's
[private vulnerability reporting form](https://github.com/ob-labs/powercontext-go/security/advisories/new).
Do not disclose vulnerability details in a public issue, discussion, pull
request, commit, or other public channel.

Include enough information for maintainers to reproduce and assess the report:

- the affected commit, build, component, and relevant configuration or build
  tags;
- the required access, trust boundary, and other attack prerequisites;
- the security impact and the behavior an attacker can observe or control;
- minimal reproduction steps or a sanitized proof of concept;
- known mitigations or a suggested repair, when available; and
- any requested disclosure constraints and how you would like to be credited.

Use synthetic or redacted examples. Never include real credentials, API
tokens, private Source or Memory content, customer data, database contents, or
unredacted sensitive local paths in a report or proof of concept.

If the issue is in a third-party dependency, follow that project's security
policy and use its confidential reporting channel as well. Submit a private
report here when the dependency issue is exploitable through PowerContext Go
or requires a repository-specific mitigation.

## What to Expect

- GitHub records the private report immediately. Maintainers aim to acknowledge
  it within five business days.
- Maintainers will validate the affected surface, attempt to reproduce the
  issue, assess severity, and request missing information when necessary.
- While an accepted report remains under active investigation, maintainers aim
  to provide a status update at least every ten business days.
- For an accepted vulnerability, maintainers will coordinate the repair,
  advisory, release or mitigation guidance, and reporter credit before public
  disclosure.
- If a report is declined, maintainers will explain whether it is out of scope,
  not reproducible, already known, or not considered a security boundary.

Response targets are not a guaranteed resolution deadline. The project does
not currently operate a vulnerability-reward or bug-bounty program.

## Coordinated Disclosure

Keep the report and all related technical details confidential while
maintainers validate the issue and prepare a reasonable repair or mitigation
path. The reporter and maintainers should coordinate a disclosure date; if
they cannot agree on timing, they should communicate before disclosure so
affected users can be protected. Maintainers will use GitHub Security
Advisories for private collaboration and publication when an advisory is
appropriate.
