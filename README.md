# PowerContext Go

PowerContext Go requires Go 1.27.0 or newer. It preserves the frozen Python
`v0.0.2` observable contract while using Go-native domain
types, lifecycle ownership, concurrency, persistence, transports, and release
packaging.

```text
module github.com/ob-labs/powercontext-go
oracle 3a6cb0151670eaff7dc0293466edd673124e80da
```

The HTTP source of truth is [`openapi/powercontext.yaml`](openapi/powercontext.yaml).
Generated code under `api/v1` and generated operation tables are never edited
by hand. Compatibility evidence lives under `test/conformance`: all 622 frozen
Python test cases are inventoried with resolvable Go or retained-host evidence.

## Repository shape

- `source`, `artifact`, `trigger`, and `inference` are lifecycle-free public
  extension contracts; `artifact/{memory,experience,skill,handoff}` contains
  the public typed Artifact families.
- `internal/{review,contextpack,handoffreport,stats,work}` contains product
  domains that are shared by the Server but are not part of the embedded Go
  SDK surface.
- `internal/runtime` owns admission, Scope boundaries, same-Scope write
  serialization, scheduled processing, and application use cases.
- `client` and `server` are public remote and process facades.
- `internal` contains product-only domains and concrete adapters: SQL,
  providers, scheduler, endpoints, HTTP, MCP, dashboard, CLI, and
  observability. Native seekDB and sqlite-vec ownership lives below
  `internal/sqlstore`.

- `integrations` contains host-native adapters for Codex, Claude Code, Bub,
  DeepSeek Harness, Hermes, LangGraph, OpenClaw, OpenCode, and Pi. They
  communicate only with the Go Server.
- `test` contains conformance, differential, and process-level suites; `tools`
  contains generators and release tooling.
- `benchmark/locomo` contains operator-facing LoCoMo configuration and result
  space; its Go runner lives in `tools/locomo`, with deterministic internals in
  `internal/benchmark/locomo`.

The deliberate public Go packages are checked against the approved pre-release
baseline under `test/api-compat`. `make api-compat` permits compatible additions
but rejects removed or incompatibly changed exported identifiers. Updating the
baseline with `make api-baseline` requires review of the compatibility impact;
the baseline is a pre-release change-control gate, not a declaration of Go v1
stability before the first release.

There is intentionally no `common`, `utils`, generic repository layer, or DI
container. Shared infrastructure exists only where it has one clear owner—for
example, privacy-safe `log/slog` setup under `internal/observability/logging`.

See [`docs/architecture/README.md`](docs/architecture/README.md) for the full
directory map and dependency rules.

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the supported Go policy, change boundaries, validation requirements, and
pull request contract.

## Build and verify

The standard build uses CGO and statically embeds the same sqlite-vec 0.1.9
`vec0` implementation as the Python runtime:

```sh
make check
make lint
make contract-test
make unit-test
make e2e-test
make build
```

Run the server with the frozen defaults:

```sh
./bin/powercontext server run
```

Server configuration uses `POWERCONTEXT_SERVER_*`; remote CLI configuration
uses `POWERCONTEXT_CLIENT_SERVER_URL`, `POWERCONTEXT_CLIENT_API_TOKEN`, and
`POWERCONTEXT_CLIENT_TIMEOUT`. The full local-embedding build additionally
requires the native tokenizer and ONNX Runtime assets described in
[`docs/release/INSTALL.md`](docs/release/INSTALL.md).
The same installation guide documents the optional native seekDB profile;
SQLite remains the zero-dependency default.

Plain HTTP is trusted only on loopback (`localhost`, `::1`, or any address in
`127.0.0.0/8`). The Server refuses an unauthenticated non-loopback bind by
default. For remote access, enable bearer authentication and terminate TLS in
front of the Server; controlled networks or deployments with upstream TLS may
instead opt in explicitly with
`POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true`.

For an authenticated non-loopback bind behind a TLS terminator, replace the
example token before starting the Server:

```sh
POWERCONTEXT_SERVER_AUTH_ENABLED=true \
POWERCONTEXT_SERVER_AUTH_TOKEN='replace-with-a-strong-token' \
./bin/powercontext server run --host 0.0.0.0
```

The Go Client likewise rejects plaintext HTTP to a non-loopback Server unless
the caller supplies its own `http.Client` and explicitly sets
`TrustTransportSecurity` for a separately secured transport.

Useful verification targets:

```sh
make lint-fix
make license-check
make pi-test
make docs-test
make test-race
make test-full TOKENIZERS_LIB_DIR=/path/to/tokenizers/lib
POWERCONTEXT_TEST_OCEANBASE_URL='mysql+aoceanbase://root%40tenant:password@127.0.0.1:2881/powercontext?charset=utf8mb4' \
  make test-oceanbase-live
```

The lint targets install the pinned `golangci-lint` release under
`.tools/bin`; its embedded gofumpt and goimports versions are therefore the same
locally and in CI. No mutable global linter installation is used.

If a newly added source file is missing the standard Apache-2.0 header, repair
all eligible files and immediately recheck them with one command:

```sh
make license-fix
```

The checked file types and deliberate generated/vendor exclusions are defined
in [`.licenserc.yaml`](.licenserc.yaml). SkyWalking Eyes is version-pinned by
the Make target and does not modify prompt text, fixtures, lock files, or
generated Go contracts.

The OceanBase target requires a dedicated disposable MySQL-mode database. It
verifies tenant and charset negotiation, the complete core and optional Report
schemas, Source cursor CAS, and Handoff Report Activity allocation against the
real server rather than a SQL mock.

The Go-native LoCoMo benchmark uses the same runtime, database, providers, and
frozen dataset contract as Python:

```sh
go run ./tools/locomo inspect --env-file benchmark/locomo/.env.example
go run ./tools/locomo run --env-file .env --run-id locomo-smoke \
  --conversation-limit 1 --question-limit 5
```

See [`benchmark/locomo/README.md`](benchmark/locomo/README.md) for resumable
ingestion, reranking, Source expansion, and independent rejudging.

Read [`AGENTS.md`](AGENTS.md) before changing package boundaries, persistence
formats, lifecycle ownership, or generated contracts.
