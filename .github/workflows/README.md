# GitHub CI/CD

The workflow topology follows the Python repository at
`3a6cb0151670eaff7dc0293466edd673124e80da`. A workflow or default-CI job belongs here only when it has a Python
counterpart or enforces a Go release constraint that does not exist in Python.

| Python workflow | Go workflow | Deliberate adaptation |
| --- | --- | --- |
| `master.yml` | `master.yml` | Pinned lint and formatting, an independent Go 1.27 readonly package build with explicit SQLite development headers, race-enabled atomic coverage, built-binary dependency-license evidence, a pinned standard-release vulnerability scan, machine-checked contribution contracts, Go module verification, vet, generated transport contracts, Go tests, and the same Pi package replace Python lock, prek, and interpreter tests. |
| `e2e-harness.yml` | `e2e-harness.yml` | The same validate/SQLite/OceanBase/evidence lifecycle drives the Go process and live OceanBase acceptance tests. |
| `license-check.yml` | `license-check.yml` | Both call SkyWalking Eyes 0.8.0 directly. `make license-check` and `make license-fix` remain local entry points. |
| `deploy-docs.yml` | `deploy-docs.yml` | Both build locked Zensical documentation and deploy GitHub Pages. |
| `build-artifacts.yml` | `build-artifacts.yml` | Go binary bundles replace Python wheel and offline-wheel bundles; standard and Full editions are release requirements. |
| `build-docker.yml` | `build-docker.yml` | Go standard and Full images replace the Python server image. |
| `release.yml` | `release.yml` | GitHub binary assets and GHCR replace PyPI; release verification and documentation deployment keep the same gates. |
| `release-verify.yml` | `release-verify.yml` | Verification exercises published Go archives and image digests instead of Python distributions. |

Four Go-specific workflows extend, rather than replace, that Python topology:

| Go workflow | Purpose |
| --- | --- |
| `migration-gates.yml` | Reusable PR assurance called by `master.yml`: explicit owned-module integrity, fresh generated-consumer builds, public API compatibility, online verification of the recorded upstream tag, release bytes, and PyPI provenance, frozen Python Oracle and exact v0.1.0 release-fixture regeneration, Python↔Go interoperability, HTTP differential, race/fuzz, live OceanBase, host adapters, evaluation, four-platform standard/Full builds, CGO-disabled portable SDK cross-builds, and the isolated downstream public-consumer workflow. |
| `codeql.yml` | Go CodeQL analysis on pull requests, pushes to `main`, a weekly schedule, and manual dispatch. Pull request runs check out the exact submitted head commit before the explicit Go build. |
| `provider-smoke.yml` | Explicitly dispatched, credentialed, bounded real-provider verification; never required on an ordinary pull request. |
| `windows-contract.yml` | Windows checkout-only contract guard: verifies LF attributes, both versioned fixture SHA-256 inventories, and generated-contract cleanliness without claiming Windows binary support. |

The committed `test/conformance/testdata/python-v0.0.2` baseline remains immutable. The separate
`test/conformance/testdata/python-v0.1.0` directory freezes exact release metadata and portable fixture evidence;
it does not silently replace the historical Oracle. Pull requests regenerate both directories from their pinned
sources, while Python-to-Go interoperability and HTTP differential checks continue to state exactly which Oracle
they exercise.

All third-party GitHub Actions are pinned to reviewed 40-character commit SHAs. The adjacent version comments retain
the human-readable update intent while preventing a mutable tag from changing executable CI code.

The `coverage` job instruments every normal Go package with race detection and `covermode=atomic`; it does not exclude
generated packages or low-coverage command surfaces. The measured baseline is 16.1% statement coverage. CI requires at
least 16.0% so ordinary rounding cannot fail a build while a material regression is still rejected. The complete
profile and function summary are retained for 14 days as bounded review evidence.
