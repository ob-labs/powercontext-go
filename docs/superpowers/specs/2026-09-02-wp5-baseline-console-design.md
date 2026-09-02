# WP5 Baseline Console Design

## Goal

Complete WP5 P3 with a usable evaluation-console workflow for immutable
Baselines and current-versus-Baseline comparisons. The console consumes the
P2 JSON API added by PR #182; it does not reimplement Baseline storage,
compatibility, comparison, or report-revision logic in TypeScript.

## Scope

- Add typed, validated `EvaluationApi` methods for the P2 Baseline endpoints.
- Add a global `/baselines` route that lists immutable Baselines and navigates
  to each source batch report.
- Add a completed-batch Baseline panel to the existing overview that can:
  create a Baseline from the displayed report revision; inspect candidates for
  OFF and ON; replace saved comparisons with compatible or warning candidates;
  and render returned current-versus-Baseline facts.
- Add React, API-client, TypeScript, and build coverage.

## Non-Goals

- Do not change the Python API, SQLite schema, worker, runner, report logic,
  CLI, OpenAPI, generated Go API, or API capabilities.
- Do not expose public single-arm batch creation. P3 operates on the paired
  batches already supported by the public console boundary.
- Do not silently select incompatible Baselines, enqueue work, run a batch,
  mutate a Baseline, or derive comparison values in the browser.

## Architecture

`EvaluationApi` remains the only browser-to-control-plane boundary. Its Zod
schemas validate Baseline records, candidate compatibility, selection updates,
and comparison responses before React receives them. URL helpers encode every
batch or Baseline identifier exactly once.

`App` gains `/baselines` alongside the existing report routes, while
`AppShell` presents the Baseline Library as a global navigation destination.
`BaselineLibrary` owns list loading, retry, empty state, and safe navigation
to a source report.

`BatchBaselinePanel` is embedded in `BatchOverview` only after a batch report
has loaded. It fetches the stored selections and candidate lists for both
paired current arms, and gets comparisons from the server. A toggle submits
the complete selected set through the replace endpoint, then reloads the
server's returned selection and comparison state. Its create form supplies the
report revision currently displayed by `BatchOverview` and generates one
idempotency key per user submission. Requests use an AbortController and
generation guard, matching the console's existing stale-response pattern.

## Data Contracts

The TypeScript contract mirrors these P2 responses:

- `BaselineRecord`: immutable identity, source batch/arm/revision, task-set,
  historical resolution counts, model/revisions, and creation time.
- `BaselineCandidate`: a record plus `compatible`, `warning`, or
  `incompatible` compatibility and reasons.
- `BaselineSelection`: `baseline_id` and current `off` or `on` arm.
- `BaselineComparisonResponse`: the current batch report revision and a list
  of server-calculated comparison records including coverage, resolution,
  outcome categories, and optional token comparisons.

The panel selects only compatible or warning candidates. Incompatible rows
remain visible with their server reason but cannot be selected. An empty
selection remains a valid replace request and yields no comparisons.

## Failure Handling

- A list or panel read failure shows a bounded retry state; it never shows
  stale data as current.
- A create or selection-write failure preserves the last confirmed server
  state and shows a compact action error.
- A create response or replacement response is reloaded from the server before
  updating comparison display.
- Aborted or superseded requests do not alter state.
- A non-completed batch shows that Baselines become available after completion;
  it does not issue candidate, selection, or comparison requests.

## Acceptance

1. The API client validates and calls every P2 Baseline endpoint with encoded
   path/query values and complete replace-selection payloads.
2. `/baselines` lists records, handles empty/error/retry states, and links to
   the correct source report route without a page reload for ordinary clicks.
3. A completed report can create a Baseline with its exact displayed revision,
   select compatible/warning candidates for an OFF or ON current arm, and
   display server-provided historical comparisons.
4. Incompatible candidates cannot be selected; failures and stale requests do
   not overwrite confirmed state.
5. Existing batch/report routes and paired comparison behavior remain intact;
   Vitest, strict TypeScript, production build, exact-Head CI, and post-main
   CI pass.
