# Install a PowerContext binary release

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

sqlite-vec 0.1.9 is statically embedded in every binary; no extension path or
host SQLite package is required. The Full archive also contains ONNX Runtime under
`lib/onnxruntime/`; set `POWERCONTEXT_ONNXRUNTIME_LIBRARY_DIR` to that
directory before selecting a `sentence-transformers:*` embedding model.

SQLite is the self-contained default and the only database in the pre-WP6
installation and acceptance scope. seekDB and OceanBase remain the final P4
backend-alignment work; their migration, packaging, license/SBOM, and release
reconciliation instructions are intentionally deferred until that scope is
accepted.

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
