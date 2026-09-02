/*
 * Copyright (c) 2026 OceanBase.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { BatchOverview } from "./BatchOverview";
import { apiStub, batchRecord, batchReport } from "../test/fixtures";
import type { BaselineCandidate, BaselineComparisonResponse, BaselineRecord, BaselineSelection } from "../types";

function baseline(baselineID: string, name: string): BaselineRecord {
  return {
    baseline_id: baselineID,
    name,
    source_batch_id: "source-batch",
    source_arm: "off",
    source_report_revision: 10_000,
    benchmark: "swebench-pro",
    task_set: "swebench-pro-public-v2",
    instance_set_digest: "a".repeat(64),
    total_tasks: 100,
    resolved_tasks: 41,
    execution_failures: 0,
    model: "gpt-5.6-sol",
    reasoning_effort: "medium",
    dataset_revision: "public-v2",
    harness_revision: "harness-sha",
    powercontext_sha: "a".repeat(40),
    codex_version: "gpt-5.6-sol",
    created_at: "2026-09-02T00:00:00Z",
  };
}

function candidate(record: BaselineRecord): BaselineCandidate {
  return { baseline: record, compatibility: { status: "compatible", reasons: [] } };
}

function comparisonResponse(batchID: string, record?: BaselineRecord): BaselineComparisonResponse {
  if (record === undefined) return { batch_id: batchID, report_revision: 10_100, comparisons: [] };
  return {
    batch_id: batchID,
    report_revision: 10_100,
    comparisons: [{
      baseline: record,
      current_arm: "off",
      compatibility: { status: "compatible", reasons: [] },
      coverage: {
        matched_tasks: 100,
        comparable_tasks: 100,
        current_execution_failures: 0,
        baseline_execution_failures: 0,
      },
      resolution: {
        baseline_resolved: 41,
        current_resolved: 48,
        total: 100,
        baseline_rate_percent: 41,
        current_rate_percent: 48,
        delta_points: 7,
      },
      outcome_categories: {
        baseline_fail_current_pass: 14,
        baseline_pass_current_fail: 7,
        both_pass: 34,
        both_fail: 45,
      },
      input_tokens: null,
      output_tokens: null,
      total_tokens: null,
    }],
  };
}

describe("BatchOverview", () => {
  it("labels a paused batch as paused", async () => {
    const api = apiStub({
      getBatch: vi.fn().mockResolvedValue(batchRecord({ status: "paused" })),
      getBatchReport: vi.fn().mockResolvedValue(batchReport),
    });

    render(<BatchOverview api={api} batchId="batch-001" navigate={() => undefined} />);

    expect(await screen.findByText("已暂停 · 100 / 100")).toBeVisible();
  });

  it("renders reconciled objective facts without authored conclusions", async () => {
    const api = apiStub({
      getBatch: vi.fn().mockResolvedValue(
        batchRecord({
          status: "completed",
          total_tasks: 100,
          started_at: "2026-07-29T01:01:00Z",
          finished_at: "2026-07-29T05:00:00Z",
          resolved_powercontext_sha: "a".repeat(40),
        }),
      ),
      getBatchReport: vi.fn().mockResolvedValue(batchReport),
    });
    render(<BatchOverview api={api} batchId="batch-001" navigate={() => undefined} />);

    expect(await screen.findByRole("heading", { name: "总体报告" })).toBeVisible();
    const summary = screen.getByLabelText("正确性汇总");
    expect(within(summary).getByText("100")).toBeVisible();
    expect(within(summary).getByText("41%")).toBeVisible();
    expect(within(summary).getByText("41 / 100 个任务")).toBeVisible();
    expect(within(summary).getByText("48%")).toBeVisible();
    expect(within(summary).getByText("48 / 100 个任务")).toBeVisible();
    expect(within(summary).getByText("+7 pp")).toBeVisible();

    expect(screen.getByRole("link", { name: /OFF 未通过.*ON 通过.*14/ })).toBeVisible();
    expect(screen.getByRole("link", { name: /OFF 通过.*ON 未通过.*7/ })).toBeVisible();
    expect(screen.getByText("可比较任务 100 / 100")).toBeVisible();
    expect(screen.getByText("72,400,000")).toBeVisible();
    expect(screen.getByText("79,800,000")).toBeVisible();
    expect(screen.getAllByText("100 / 100 个任务有记录")).toHaveLength(3);
    expect(screen.queryByText(/提升|退化|验收有效|验收无效|优先分析|建议/)).not.toBeInTheDocument();
    expect(screen.queryByText(/N\/A|补丁大小|生命周期|处理有效性/)).not.toBeInTheDocument();
  });

  it("opens the exact task filter from a pair category", async () => {
    const navigate = vi.fn();
    render(<BatchOverview api={apiStub()} batchId="batch-001" navigate={navigate} />);

    fireEvent.click(await screen.findByRole("link", { name: /OFF 通过.*ON 未通过.*7/ }));

    expect(navigate).toHaveBeenCalledWith(
      "/report/batch-001/tasks?category=off_pass_on_fail",
    );
  });

  it("clears A baseline state before B confirmation and never writes A selections to B", async () => {
    const oldBaseline = baseline("baseline-a", "Baseline A");
    const currentBaseline = baseline("baseline-b", "Baseline B");
    let resolveBCandidates!: (value: BaselineCandidate[]) => void;
    let resolveBSelections!: (value: BaselineSelection[]) => void;
    let resolveBComparisons!: (value: BaselineComparisonResponse) => void;
    const bCandidates = new Promise<BaselineCandidate[]>((resolve) => { resolveBCandidates = resolve; });
    const bSelections = new Promise<BaselineSelection[]>((resolve) => { resolveBSelections = resolve; });
    const bComparisons = new Promise<BaselineComparisonResponse>((resolve) => { resolveBComparisons = resolve; });
    const replaceBaselineSelections = vi.fn().mockResolvedValue([]);
    const api = apiStub({
      getBatch: vi.fn().mockImplementation((batchID) => Promise.resolve(batchRecord({ batch_id: batchID, status: "completed" }))),
      getBatchReport: vi.fn().mockImplementation((batchID) => Promise.resolve({ ...batchReport, batch_id: batchID })),
      getBaselineCandidates: vi.fn().mockImplementation((batchID, arm) => {
        if (batchID === "batch-a") return Promise.resolve(arm === "off" ? [candidate(oldBaseline)] : []);
        return arm === "off" ? bCandidates : Promise.resolve([]);
      }),
      getBaselineSelections: vi.fn().mockImplementation((batchID) => (
        batchID === "batch-a" ? Promise.resolve([]) : bSelections
      )),
      getBaselineComparisons: vi.fn().mockImplementation((batchID) => (
        batchID === "batch-a" ? Promise.resolve(comparisonResponse(batchID, oldBaseline)) : bComparisons
      )),
      replaceBaselineSelections,
    });
    const user = userEvent.setup();
    const { rerender } = render(<BatchOverview api={api} batchId="batch-a" navigate={() => undefined} />);

    expect(await screen.findByRole("checkbox", { name: /baseline A.*OFF/i })).toBeEnabled();

    rerender(<BatchOverview api={api} batchId="batch-b" navigate={() => undefined} />);
    await waitFor(() => expect(api.getBaselineSelections).toHaveBeenCalledWith("batch-b", expect.any(AbortSignal)));

    expect(screen.queryByRole("checkbox", { name: /baseline A.*OFF/i })).not.toBeInTheDocument();
    expect(screen.queryAllByText("Baseline A")).toHaveLength(0);
    expect(replaceBaselineSelections).not.toHaveBeenCalled();

    resolveBCandidates([candidate(currentBaseline)]);
    resolveBSelections([]);
    resolveBComparisons(comparisonResponse("batch-b", currentBaseline));
    const current = await screen.findByRole("checkbox", { name: /baseline B.*OFF/i });
    await user.click(current);

    expect(replaceBaselineSelections).toHaveBeenLastCalledWith(
      "batch-b",
      [{ baseline_id: "baseline-b", current_arm: "off" }],
      expect.any(AbortSignal),
    );
  });

  it("unlocks B after an in-flight A selection is abandoned by a batch switch", async () => {
    const oldBaseline = baseline("baseline-a", "Baseline A");
    const currentBaseline = baseline("baseline-b", "Baseline B");
    let resolveASelection!: (value: BaselineSelection[]) => void;
    const aSelection = new Promise<BaselineSelection[]>((resolve) => { resolveASelection = resolve; });
    let aSelectionReads = 0;
    const replaceBaselineSelections = vi.fn().mockResolvedValue([]);
    const api = apiStub({
      getBatch: vi.fn().mockImplementation((batchID) => Promise.resolve(batchRecord({ batch_id: batchID, status: "completed" }))),
      getBatchReport: vi.fn().mockImplementation((batchID) => Promise.resolve({ ...batchReport, batch_id: batchID })),
      getBaselineCandidates: vi.fn().mockImplementation((batchID, arm) => Promise.resolve(
        arm === "off" ? [candidate(batchID === "batch-a" ? oldBaseline : currentBaseline)] : [],
      )),
      getBaselineSelections: vi.fn().mockImplementation((batchID) => (
        batchID === "batch-a" && ++aSelectionReads > 1 ? aSelection : Promise.resolve([])
      )),
      getBaselineComparisons: vi.fn().mockImplementation((batchID) => Promise.resolve(comparisonResponse(batchID))),
      replaceBaselineSelections,
    });
    const user = userEvent.setup();
    const { rerender } = render(<BatchOverview api={api} batchId="batch-a" navigate={() => undefined} />);

    const old = await screen.findByRole("checkbox", { name: /baseline A.*OFF/i });
    await user.click(old);
    await waitFor(() => expect(replaceBaselineSelections).toHaveBeenCalledWith(
      "batch-a",
      [{ baseline_id: "baseline-a", current_arm: "off" }],
      expect.any(AbortSignal),
    ));

    rerender(<BatchOverview api={api} batchId="batch-b" navigate={() => undefined} />);
    const current = await screen.findByRole("checkbox", { name: /baseline B.*OFF/i });
    expect(current).toBeEnabled();
    await user.click(current);

    expect(replaceBaselineSelections).toHaveBeenLastCalledWith(
      "batch-b",
      [{ baseline_id: "baseline-b", current_arm: "off" }],
      expect.any(AbortSignal),
    );

    resolveASelection([]);
  });
});
