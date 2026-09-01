# Install a PowerContext binary release

## Upstream v0.1.0 alignment contract

The current PowerContext Go alignment target is the upstream PowerContext
v0.1.0 release, not a published PowerContext Go binary. PowerContext Go has no
published release archive or image yet, so do not treat the upstream Python
wheel or source distribution as a Go installation input. They are the
independently verifiable provenance for the behavior being aligned.

| Provenance | Exact value | Verification use |
| --- | --- | --- |
| Upstream release | [`powercontext-v0.1.0`](https://github.com/oceanbase/powercontext/releases/tag/powercontext-v0.1.0) at `7b736206a53a6de6f43d4b517893ee1a80e7183d` | Current parity identity; do not substitute a moving upstream branch. |
| Wheel | `powercontext-0.1.0-py3-none-any.whl` | SHA-256 `94f8fef36d4afcee09dd5231fbe5edfe47e42e41994596b775e2f203ef6fac72`. |
| Source distribution | `powercontext-0.1.0.tar.gz` | SHA-256 `18d47a335340b0870216e2cc0fb1fd8e4d865880155daeea3b01187c950fd746`. |
| Python distribution provenance | [PyPI `powercontext` 0.1.0](https://pypi.org/project/powercontext/0.1.0/) | Cross-check the pinned release asset before changing parity fixtures or assertions. |

When the first PowerContext Go release is published, obtain its archive and
container digests from that Go release only. The release-verification workflow
will then verify its release-level checksums, SBOM, archive contents, version
metadata, and bounded CLI, Server, MCP, Memory, restart, and shutdown smokes.

Every archive is self-describing and contains the CLI/Server binary, the
authoritative OpenAPI document, `.env.example`, retained host-adapter assets,
embedded sqlite-vec, dependency licenses, build metadata, an SPDX JSON SBOM,
and an internal `SHA256SUMS` file.

Release verification checks the packaged `.env.example` before starting the
binary. Its security-sensitive defaults keep the Server and Client on
`127.0.0.1`, leave bearer authentication disabled for that loopback-only
configuration, and keep unauthenticated non-loopback access disabled. Enabling
remote access requires an explicit host change together with authentication or
the documented controlled-network opt-in; never ship an active default bearer
token in the example file.

Verify the downloaded archive and its detached SBOM against the release-level
`SHA256SUMS`, extract it, then verify the files inside the archive:

```sh
sha256sum --check SHA256SUMS
tar -xzf powercontext-*.tar.gz
cd powercontext-*/
sha256sum --check SHA256SUMS
./bin/powercontext --version
```

On macOS, use `shasum -a 256 -c SHA256SUMS` in place of `sha256sum`.

## Configure the SQLite pre-WP6 installation

SQLite is the zero-dependency default and the only database in the pre-WP6
installation and acceptance scope. Create, inspect, and validate the managed
environment file before starting the Server:

```sh
./bin/powercontext config init --non-interactive
./bin/powercontext config show --env-file .env
./bin/powercontext config validate --env-file .env
./bin/powercontext server run
```

`config show` redacts credentials. `config validate` checks configuration
syntax, persistent storage paths, and constructible Server settings before the
Server owns the SQLite database. Keep the environment file and the SQLite data
under the operator's backup policy; do not commit either file or credentials.

sqlite-vec 0.1.9 is statically embedded in every binary; no extension path or
host SQLite package is required. The Full archive also contains ONNX Runtime under
`lib/onnxruntime/`; set `POWERCONTEXT_ONNXRUNTIME_LIBRARY_DIR` to that
directory before selecting a `sentence-transformers:*` embedding model.

SQLite is the self-contained default and the only database in the pre-WP6
installation and acceptance scope. seekDB and OceanBase remain the final P4
backend-alignment work; their migration, packaging, license/SBOM, and release
reconciliation instructions are intentionally deferred until that scope is
accepted.

## Upgrade and projection safety

Before replacing a binary or changing an embedding dimension, stop the Server,
retain a recoverable copy of the SQLite database and its configuration, then
validate the candidate configuration and start the new binary. Immutable
Artifact revisions, Memory manifests and entry versions, and Source journal
state are authoritative. FTS and vector indexes are rebuildable projections
derived from that authority state.

If startup reports either `sqlite-vec projection dimension ... does not match
configured dimension ...; rebuild the projection` or `sqlite-vec projection
schema is incompatible; rebuild the projection`, preserve the database and the
reported dimensions. Do not manually drop, truncate, edit, or migrate the
projection tables, and do not repeatedly restart the Server against the same
mismatch. The pre-WP6 product has no supported automatic or operator-facing
vector-projection rebuild command. Recover with the previously working
configuration or backup, then use an explicitly documented future maintenance
procedure; that procedure must rebuild only projections and must not rewrite
the authoritative revisions or manifests.

## Transport and host operation

Plain HTTP is trusted only on `localhost`, `::1`, and the full `127.0.0.0/8`
range. The Server rejects an unauthenticated non-loopback bind by default. For
remote access, enable bearer authentication, use a strong secret outside the
environment file, and terminate TLS before the Server. A controlled network or
upstream TLS deployment may set
`POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true`, but that opt-in
does not add authentication or encryption. The Go Client also refuses remote
plaintext unless its caller supplies an HTTP client and explicitly vouches for
the separately secured transport with `TrustTransportSecurity`.

Before WP6, Codex and WorkBuddy are the supported host-operation scope. Both
integrations call a running Server; neither embeds SQLite or starts the Server.
Use the [Codex integration guide](https://github.com/ob-labs/powercontext-go/blob/main/integrations/codex/plugins/powercontext/README.md)
for MCP, hook, scope, and credential-backed header configuration. Install
WorkBuddy with `./bin/powercontext setup workbuddy`, then run
`./bin/powercontext doctor workbuddy` to check its Hook, MCP registration,
managed Skill, and Server health without exposing credentials or prompt data.
OpenCode, DSH, LangChain, Pydantic AI, Hermes, and other retained adapters
remain post-WP6 work and are not covered by this installation contract.

The binary itself does not require a Python runtime. This monorepo tracks
Python and TypeScript assets for host-native integrations and the evaluation
control plane, but they are not Go binary runtime requirements. Before WP6,
the supported acceptance matrix is Codex, WorkBuddy, and SQLite. Other
retained host adapters are isolated from the Go implementation, call the Go
Server over HTTP or MCP, and remain in the post-WP6 P3 work plan.

The container images bind `0.0.0.0:8000` and declare the explicit
controlled-network opt-in required for a published port. That opt-in does not
provide authentication or encryption. Before exposing a container outside a
controlled network, enable `POWERCONTEXT_SERVER_AUTH_ENABLED`, configure a
strong `POWERCONTEXT_SERVER_AUTH_TOKEN`, and terminate TLS in front of it.
