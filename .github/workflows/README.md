# GitHub CI/CD

The workflow topology follows the Python repository at
`3a6cb0151670eaff7dc0293466edd673124e80da`. A workflow or default-CI job belongs here only when it has a Python
counterpart or enforces a Go release constraint that does not exist in Python.

| Python workflow | Go workflow | Deliberate adaptation |
| --- | --- | --- |
| `master.yml` | `master.yml` | Pinned lint and formatting, an independent Go 1.27 readonly package build with explicit SQLite development headers, race-enabled atomic coverage, built-binary dependency-license evidence, a pinned standard-release vulnerability scan, machine-checked contribution contracts, Go module verification, vet, generated transport contracts, and Go tests. |
| `e2e-harness.yml` | `e2e-harness.yml` | The validate/SQLite/evidence lifecycle drives the supported Go process acceptance test. |
| `license-check.yml` | `license-check.yml` | Both call SkyWalking Eyes 0.8.0 directly. `make license-check` and `make license-fix` remain local entry points. |
| `deploy-docs.yml` | `deploy-docs.yml` | Both build locked Zensical documentation and deploy GitHub Pages. |
| `build-artifacts.yml` | `build-artifacts.yml` | Go binary bundles replace Python wheel and offline-wheel bundles; standard and Full editions are release requirements. |
| `build-docker.yml` | `build-docker.yml` | Go standard and Full images replace the Python server image. |
| `release.yml` | `release.yml` | GitHub binary assets and GHCR replace PyPI; Linux and macOS retain Standard and Full archives, while Windows adds one Standard-only `windows-amd64` archive. Release verification and documentation deployment keep the same gates. |
| `release-verify.yml` | `release-verify.yml` | Verification exercises published Go archives and image digests instead of Python distributions, including a `windows-2025` consumer of the published Windows Standard archive. |

Six Go-specific workflows extend, rather than replace, that Python topology:

| Go workflow | Purpose |
| --- | --- |
| `migration-gates.yml` | Reusable PR assurance called by `master.yml`: explicit owned-module integrity, fresh generated-consumer builds, public API compatibility, online verification of the recorded upstream tag, release bytes, and PyPI provenance, frozen Python Oracle and exact v0.1.0 release-fixture regeneration, Python↔Go interoperability, HTTP differential, race/fuzz, Codex/WorkBuddy host-adapter evidence, Codex/SQLite evaluation, four-platform standard/Full builds, CGO-disabled portable SDK cross-builds, and the isolated downstream public-consumer workflow. |
| `codeql.yml` | Go CodeQL analysis on pull requests, pushes to `main`, a weekly schedule, and manual dispatch. Pull request runs check out the exact submitted head commit before the explicit Go build. |
| `scorecard.yml` | Advisory OpenSSF Scorecard analysis on default-branch, branch-protection, scheduled, and manual events. It publishes pinned-action supply-chain findings as SARIF without turning a mutable numeric score into a merge contract. |
| `nightly-reliability.yml` | Scheduled and manually dispatched long-running reliability evidence: five independent four-minute fuzz targets, a stable aggregate fuzz result, and repeated race/shuffle tests with bounded flaky-test statistics and failure output. |
| `provider-smoke.yml` | Explicitly dispatched, credentialed, bounded real-provider verification; never required on an ordinary pull request. |
| `windows-contract.yml` | Pre-release Windows guard: verifies targeted Go regressions, LF and fixture contracts, and a locally built Standard archive's real Task Scheduler lifecycle. This checkout-built archive is not published-release consumer evidence. |

Release-line governance is checked by `make governance-check` in the `governance-contract` job and by the explicit
`release-contract` job. The release-contract job also runs `make release-contract-check` against the recorded upstream
release tag, assets, and PyPI provenance. Together they keep `docs/release/POLICY.md`, `.github/release.yml`, the
`release.yml` tag/manual-dispatch triggers, and exact release metadata aligned so branch/version/tag/backport/release-note
consistency does not depend on prose review alone.

The committed `test/conformance/testdata/python-v0.0.2` baseline remains immutable. The separate
`test/conformance/testdata/python-v0.1.0` directory freezes exact release metadata and portable fixture evidence;
it does not silently replace the historical Oracle. Pull requests regenerate both directories from their pinned
sources, while Python-to-Go interoperability and HTTP differential checks continue to state exactly which Oracle
they exercise.

Within `migration-gates.yml`, host and evaluation evidence has two explicit
contracts:

| Job ID and display name | Scope | Acceptance role |
| --- | --- | --- |
| `host-adapters` / `Pre-WP6 host adapters` | Codex and WorkBuddy | Stable supported host gate; it must run for every pull request. |
| `evaluation` / `Codex/SQLite evaluation control plane` | Python control plane and frontend for Codex/SQLite evaluation | Evidence for the supported evaluation surface. |

Repository administrators should retain the stable `Pre-WP6 host adapters`
required check. Unsupported backend and retained-host jobs are not active
workflow checks.

All third-party GitHub Actions are pinned to reviewed 40-character commit SHAs. The adjacent version comments retain
the human-readable update intent while preventing a mutable tag from changing executable CI code.

Dependabot monitors every tracked Go module, npm/pnpm project, uv project, the root Dockerfile, and GitHub Actions.
The exact owned directory sets and staggered weekly schedules are enforced by `make governance-check`; adding or
removing a tracked dependency surface requires updating that explicit contract in the same reviewed change.

The release workflow retains BuildKit's maximum OCI provenance and SBOM attestations, and additionally creates signed
GitHub build provenance in the jobs that produce the final archive bytes, checksum manifests, and image digests. A
manual release must be dispatched from the exact release tag ref so the signer identity cannot drift from the released
source. `release-verify.yml` binds verification to this repository and `release.yml`, verifies every downloaded subject
and immutable OCI digest before extraction or execution, and only then runs the published process surfaces.
The binary matrix publishes eight existing Linux/macOS Standard and Full
archives plus one Windows Standard archive, with nine detached SPDX SBOMs, five
platform checksum manifests, and 24 entries in the release-level checksum
manifest. The Windows verification job downloads its archive, SBOM, and both
checksum manifests from the published GitHub Release, verifies signed
provenance before extraction, checks `windows-amd64` Standard build metadata,
and exercises Task Scheduler install/status/uninstall from that downloaded
binary. It does not publish or accept Windows Full, Windows native inference,
Linux systemd/user D-Bus/desktop lifecycle, or macOS LaunchAgent evidence.

The `coverage` job instruments every normal Go package with race detection and `covermode=atomic`; it does not exclude
generated packages or low-coverage command surfaces. The measured baseline is 16.1% statement coverage. CI requires at
least 16.0% so ordinary rounding cannot fail a build while a material regression is still rejected. The complete
profile and function summary are retained for 14 days as bounded review evidence.
