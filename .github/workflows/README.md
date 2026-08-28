# GitHub CI/CD

The workflow topology follows the Python repository at
`3a6cb0151670eaff7dc0293466edd673124e80da`. A workflow or default-CI job belongs here only when it has a Python
counterpart or enforces a Go release constraint that does not exist in Python.

| Python workflow | Go workflow | Deliberate adaptation |
| --- | --- | --- |
| `master.yml` | `master.yml` | Go module, formatting, vet, generated transport contracts, Go tests, and the same Pi package replace Python lock, prek, and interpreter tests. |
| `e2e-harness.yml` | `e2e-harness.yml` | The same validate/SQLite/OceanBase/evidence lifecycle drives the Go process and live OceanBase acceptance tests. |
| `license-check.yml` | `license-check.yml` | Both call SkyWalking Eyes 0.8.0 directly. `make license-check` and `make license-fix` remain local entry points. |
| `deploy-docs.yml` | `deploy-docs.yml` | Both build locked Zensical documentation and deploy GitHub Pages. |
| `build-artifacts.yml` | `build-artifacts.yml` | Go binary bundles replace Python wheel and offline-wheel bundles; standard and Full editions are release requirements. |
| `build-docker.yml` | `build-docker.yml` | Go standard and Full images replace the Python server image. |
| `release.yml` | `release.yml` | GitHub binary assets and GHCR replace PyPI; release verification and documentation deployment keep the same gates. |
| `release-verify.yml` | `release-verify.yml` | Verification exercises published Go archives and image digests instead of Python distributions. |

Three Go-specific workflows extend, rather than replace, that Python topology:

| Go workflow | Purpose |
| --- | --- |
| `migration-gates.yml` | Reusable PR assurance called by `master.yml`: frozen Python Oracle regeneration, Python↔Go interoperability, HTTP differential, race/fuzz, live OceanBase, host adapters, evaluation, and four-platform standard/Full builds. |
| `codeql.yml` | Go CodeQL analysis on pull requests, pushes to `main`, a weekly schedule, and manual dispatch. Pull request runs check out the exact submitted head commit before the explicit Go build. |
| `provider-smoke.yml` | Explicitly dispatched, credentialed, bounded real-provider verification; never required on an ordinary pull request. |

The committed `test/conformance/testdata/python-v0.0.2` baseline remains immutable. Pull requests execute the pinned
Python Oracle to prove that regenerated portable fixtures, database interoperability, and HTTP behavior still match;
they do not silently replace the committed baseline.

All third-party GitHub Actions are pinned to reviewed 40-character commit SHAs. The adjacent version comments retain
the human-readable update intent while preventing a mutable tag from changing executable CI code.
