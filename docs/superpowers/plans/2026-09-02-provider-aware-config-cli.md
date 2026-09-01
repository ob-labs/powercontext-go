# Provider-Aware Config CLI Implementation Plan

**Goal:** Align Go `config init`, `config show`, and `config validate` with the v0.1.0 provider-aware configuration contract without widening the Server's supported provider set or exposing credentials.

**Scope:** The sixteen pending cases in `tests/test_config_cli.py` at the exact v0.1.0 target. This plan owns only `internal/cli` configuration collection, managed environment rendering/parsing, redaction, and provider constructibility checks. It does not add a provider adapter, change Server runtime configuration semantics, or add retained-agent behavior.

## Decisions

- Preserve the existing managed-block markers and atomic `0600` write behavior.
- Replace the fixed OpenRouter-only values with a CLI-local configuration value containing a generation model, embedding model, provider environment assignments, and explicit credential names.
- Treat provider prefixes as data first: known model-provider routes must pass the Go route/factory boundary; unknown provider/model identifiers fail before writing. Provider environment variable names remain extensible when syntactically valid.
- Permit a configured HTTP base URL only when it contains no embedded user information. Provider error text, URLs, credentials, and values never appear in `show`, response output, or stable validation errors.
- Keep an explicit credential-name list in the managed block. `show` redacts this list in addition to the existing suffix/container redaction policy.
- `init` refuses to overwrite any existing file without `--force`; a force update preserves unrelated assignments and replaces only the managed values.
- A successful `init` writes only after the complete configuration validates. Invalid dimensions, duplicate names, missing required credentials, invalid routes, and Server-invalid values leave no new output file.

## Tasks

1. Add RED tests for a provider-aware generated configuration: OpenAI defaults, arbitrary supported provider assignments, base URL credential rejection, duplicate assignments, numeric errors, credential metadata/redaction, and overwrite protection.
2. Introduce CLI-local model/variable/configuration values and pure validation/rendering helpers. Keep them independent from terminal I/O.
3. Refactor `config init` to collect defaults or terminal input into the configuration value, validate it, and render the managed environment block with metadata.
4. Refactor `config show` and `config validate` to reconstruct configuration metadata, preserve redaction, and provide actionable usage errors without a traceback.
5. Add exact case mappings, regenerate the v0.1.0 inventory, and run targeted CLI, model-provider, Server configuration, generator, lint, and module checks.
6. Refresh the base, push an isolated PR, require exact-Head CI/review, verify the server merge and five post-main workflows, then update Issue #3 only from the merged main evidence.

## Constraints

- No provider request, credential logging, persistence write, or external process is needed to validate the configuration model.
- Existing noninteractive invocation remains supported and produces a usable default configuration without credentials.
- Do not make a custom connection inherit a provider credential merely because its model text happens to use a known prefix.
- Do not introduce a broad provider allowlist distinct from `internal/modelprovider`; accept only the existing Server-provider route contract when constructibility is claimed.
- All commits use the Lore message format and the OmX co-author trailer.
