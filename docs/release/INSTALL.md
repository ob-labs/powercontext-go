# Install a PowerContext binary release

Every archive is self-describing and contains the CLI/Server binary, the
authoritative OpenAPI document, `.env.example`, all nine host adapters, embedded
sqlite-vec, dependency licenses, build metadata, an SPDX JSON SBOM, and an
internal `SHA256SUMS` file.

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

SQLite is the self-contained default. Embedded seekDB is optional on Linux and
macOS and requires a native `libseekdb` package from the official
[`oceanbase/seekdb-bindings`](https://github.com/oceanbase/seekdb-bindings)
release. Keep the `seekdb` executable beside `libseekdb.so` (or
`libseekdb.dylib`). Either place that library beside the PowerContext binary or
set its explicit path, then select the profile:

```sh
export POWERCONTEXT_SERVER_DATABASE_KIND=seekdb
export POWERCONTEXT_SERVER_DATABASE_LIBRARY_PATH=/opt/seekdb/lib/libseekdb.so
# Optional; defaults to $POWERCONTEXT_HOME/seekdb.
export POWERCONTEXT_SERVER_DATABASE_PATH=/var/lib/powercontext/seekdb
./bin/powercontext server run
```

The server fails closed if the native library is missing, incompatible, or
does not return a valid local connection profile; it never silently falls back
to a different database.

The binary itself does not require a Python runtime. Codex, Claude Code, Bub,
Hermes, and LangGraph retain host-native Python adapters; DSH, OpenClaw,
OpenCode, and Pi retain TypeScript adapters. Each adapter is isolated from the
Go implementation and calls the Go Server over HTTP or MCP.

The container images bind `0.0.0.0:8000` and declare the explicit
controlled-network opt-in required for a published port. That opt-in does not
provide authentication or encryption. Before exposing a container outside a
controlled network, enable `POWERCONTEXT_SERVER_AUTH_ENABLED`, configure a
strong `POWERCONTEXT_SERVER_AUTH_TOKEN`, and terminate TLS in front of it.
