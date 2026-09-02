# WP5 Baseline Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`
> task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver the WP5 P3 Baseline Library and current-versus-Baseline
console flows on the P2 API contract.

**Architecture:** Add strictly validated Baseline methods to `EvaluationApi`, a
global library route, and one completed-batch panel that delegates creation,
selection replacement, candidate compatibility, and comparison facts to the
server. Browser state only represents confirmed P2 responses.

**Tech Stack:** React 19, TypeScript 5.9 strict mode, Zod 4, Vitest,
Testing Library, Vite.

**Spec:** `docs/superpowers/specs/2026-09-02-wp5-baseline-console-design.md`

## Global Constraints

- Consume PR #182's JSON routes; do not modify Python P1/P2a/P2b behavior.
- Preserve paired public console behavior and do not expose single-arm batch
  creation through P3.
- Use complete server values for Baselines and comparisons; do not calculate
  compatibility, revisions, or historical deltas in React.
- Keep navigation client-side for ordinary clicks and preserve modifier-click
  browser behavior.
- Test all browser API payloads through `EvaluationApi` rather than source-text
  assertions; run `npm test`, `npm run build`, and exact-Head CI.

---

### Task 1: Add Validated Baseline API Client Contracts

**Files:**
- Modify: `evaluation/web/src/types.ts`
- Modify: `evaluation/web/src/api.ts`
- Modify: `evaluation/web/src/api.test.ts`

**Interfaces:**
- Produces `BaselineRecord`, `BaselineCandidate`, `BaselineSelection`, and
  `BaselineComparisonResponse` TypeScript types matching P2 responses.
- Produces `EvaluationApi.listBaselines`, `createBaseline`,
  `getBaselineCandidates`, `getBaselineSelections`,
  `replaceBaselineSelections`, and `getBaselineComparisons`.

- [x] **Step 1: Write failing API-client tests**

```ts
await api.createBaseline({ name: "Release", source_batch_id: "batch/a", source_arm: "on", expected_report_revision: 217, idempotency_key: "baseline-create-217" });
expect(fetch).toHaveBeenCalledWith("/api/baselines", expect.objectContaining({ method: "POST" }));

await api.replaceBaselineSelections("batch/a", [{ baseline_id: "baseline/a", current_arm: "off" }]);
expect(fetch).toHaveBeenCalledWith("/api/batches/batch%2Fa/baseline-selections", expect.objectContaining({ method: "PUT" }));
```

- [x] **Step 2: Run the API-client tests and verify they fail**

Run: `npm test -- api.test.ts`

Expected: missing Baseline methods or invalid-response failures.

- [x] **Step 3: Add Zod schemas, types, URL helpers, and API methods**

Implement all six methods through `#json`, encode identifiers with
`encodeURIComponent`, serialize full replace-selection arrays, and validate
arrays and nested compatibility/comparison records before returning them.

- [x] **Step 4: Run API-client tests and verify they pass**

Run: `npm test -- api.test.ts`

Expected: request paths, payloads, and invalid-payload rejection pass.

- [x] **Step 5: Commit the typed API contract**

```text
feat(evaluation-web): add baseline API client contracts
```

### Task 2: Add the Baseline Library Route

**Files:**
- Create: `evaluation/web/src/components/BaselineLibrary.tsx`
- Create: `evaluation/web/src/components/BaselineLibrary.test.tsx`
- Modify: `evaluation/web/src/App.tsx`
- Modify: `evaluation/web/src/components/AppShell.tsx`
- Modify: `evaluation/web/src/App.test.tsx`
- Modify: `evaluation/web/src/styles.css`

**Interfaces:**
- Consumes: `EvaluationApi.listBaselines(signal)` and `BaselineRecord`.
- Produces: client route `/baselines`, global navigation link, list/empty/error
  states, and source batch links `/report/{encoded source_batch_id}`.

- [x] **Step 1: Write failing route and library tests**

```tsx
render(<App api={apiStub({ listBaselines: vi.fn().mockResolvedValue([baseline]) })} />);
await userEvent.click(screen.getByRole("link", { name: "基线库" }));
expect(await screen.findByRole("heading", { name: "基线库" })).toBeVisible();
expect(screen.getByRole("link", { name: "查看总体报告" })).toHaveAttribute("href", "/report/batch%2Fsource");
```

- [x] **Step 2: Run the new tests and verify they fail**

Run: `npm test -- BaselineLibrary.test.tsx App.test.tsx`

Expected: missing route, navigation link, or component.

- [x] **Step 3: Implement route, library loading, and navigation**

Extend `Route` with `baselines`, add `/baselines` parsing before report
fallbacks, preserve modifier clicks, use AbortController/generation guarding,
and render a compact table with empty/error/retry states.

- [x] **Step 4: Run route and library tests and verify they pass**

Run: `npm test -- BaselineLibrary.test.tsx App.test.tsx`

Expected: list, empty state, retry, encoded source link, and navigation pass.

- [x] **Step 5: Commit the library route**

```text
feat(evaluation-web): add baseline library route
```

### Task 3: Add Completed-Batch Baseline Creation and Comparison Panel

**Files:**
- Create: `evaluation/web/src/components/BatchBaselinePanel.tsx`
- Create: `evaluation/web/src/components/BatchBaselinePanel.test.tsx`
- Modify: `evaluation/web/src/components/BatchOverview.tsx`
- Modify: `evaluation/web/src/styles.css`

**Interfaces:**
- Consumes: loaded `BatchRecord`, `BatchReport.report_revision`, `EvaluationApi`
  Baseline methods, and paired current arms.
- Produces: completed-batch creation, candidate display, full selection
  replacement, and server-calculated comparison rendering.

- [x] **Step 1: Write failing panel tests**

```tsx
render(<BatchBaselinePanel api={api} batch={completedBatch} report={report} />);
await userEvent.click(screen.getByRole("checkbox", { name: /baseline release.*OFF/i }));
expect(api.replaceBaselineSelections).toHaveBeenCalledWith(completedBatch.batch_id, [{ baseline_id: "baseline-release", current_arm: "off" }], expect.any(AbortSignal));
expect(await screen.findByText("历史解决率" )).toBeVisible();
```

- [x] **Step 2: Run the panel tests and verify they fail**

Run: `npm test -- BatchBaselinePanel.test.tsx`

Expected: missing component or selection replacement call.

- [x] **Step 3: Implement server-backed panel state**

Load OFF/ON candidates, stored selections, and comparisons only for completed
batches. Generate one idempotency key per create submit; submit the exact
`report.report_revision`; disable incompatible candidates; replace the complete
selection array after a toggle; then reload confirmed selections and
comparisons. Abort stale reads and keep last confirmed state on action errors.

- [x] **Step 4: Embed the panel in `BatchOverview` and add styles**

Place it after existing factual report sections, preserve existing KPI/pair
cards, use tables and compact status labels, and stack controls responsively
within the existing console breakpoints.

- [x] **Step 5: Run component tests and verify they pass**

Run: `npm test -- BatchBaselinePanel.test.tsx BatchOverview.test.tsx`

Expected: create revision, selection replacement, incompatibility disablement,
comparison facts, retry/error, and existing overview tests pass.

- [x] **Step 6: Commit the completed-batch flow**

```text
feat(evaluation-web): add baseline comparison panel
```

### Task 4: Run the P3 Frontend Delivery Gates

**Files:**
- Modify only files created or changed in Tasks 1-3 when a gate exposes a
  P3 defect.

- [x] **Step 1: Run the complete frontend unit suite**

Run: `npm test`

Expected: all React and API-client tests pass.

- [x] **Step 2: Run strict production validation**

Run: `npm run build`

Expected: TypeScript project build and Vite production bundle succeed.

- [x] **Step 3: Run affected Python API coverage**

Run: `uv run --offline --project evaluation pytest -c evaluation/pyproject.toml evaluation/tests/web/test_api.py -q`

Expected: Baseline endpoints remain green.

- [x] **Step 4: Inspect final diff and commit delivery artifacts**

Run: `git diff --check` and `git status --short`

Expected: only P3 console/design/plan files change.

- [ ] **Step 5: Open the P3 PR and verify exact-Head then post-main CI**

Expected: current main is the PR base; control-plane frontend build is green
on the exact Head and after squash merge before changing Issue #3 tracking.
