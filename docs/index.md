# PowerContext Go

PowerContext Go is the Go 1.25 implementation of PowerContext. It preserves the public HTTP, MCP, persistence, CLI,
Dashboard, integration, and release contracts while using Go-native domain and runtime boundaries.

Start with the [architecture overview](architecture/README.md), then use the
[release installation guide](release/INSTALL.md) to install a published binary bundle.

For an operator-facing Source to Memory verification loop, use the
[English full-capability guide](en/docs/how-to/full-capability-runtime.md) or
[Chinese full-capability guide](zh/docs/how-to/full-capability-runtime.md).

The OpenAPI contract in `openapi/powercontext.yaml` is the transport source of truth. The repository root
[`README.md`](https://github.com/ob-labs/powercontext-go/blob/main/README.md) contains local build and development
commands.
