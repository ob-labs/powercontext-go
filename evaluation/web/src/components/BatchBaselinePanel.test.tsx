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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { BatchBaselinePanel } from "./BatchBaselinePanel";
import { apiStub, batchRecord, batchReport, deferred } from "../test/fixtures";
import type { BaselineComparisonResponse, BaselineSelection } from "../types";

const baseline = {
  baseline_id: "baseline-release",
  name: "Baseline release",
  source_batch_id: "batch-source",
  source_arm: "off" as const,
  source_report_revision: 10_000,
  benchmark: "swebench-pro" as const,
  task_set: "swebench-pro-public-v2" as const,
  instance_set_digest: "a".repeat(64),
  total_tasks: 100,
  resolved_tasks: 41,
  execution_failures: 0,
  model: "gpt-5.6-sol",
  reasoning_effort: "medium" as const,
  dataset_revision: "public-v2",
  harness_revision: "harness-sha",
  powercontext_sha: "a".repeat(40),
  codex_version: "gpt-5.6-sol",
  created_at: "2026-09-02T00:00:00Z",
};

describe("BatchBaselinePanel", () => {
  it("replaces the complete OFF selection and renders server comparison facts", async () => {
    const replaceBaselineSelections = vi.fn().mockResolvedValue([
      { baseline_id: "baseline-release", current_arm: "off" },
    ]);
    const api = apiStub({
      getBaselineCandidates: vi.fn().mockResolvedValue([
        { baseline, compatibility: { status: "compatible", reasons: [] } },
      ]),
      getBaselineSelections: vi.fn().mockResolvedValue([]),
      replaceBaselineSelections,
      getBaselineComparisons: vi.fn().mockResolvedValue({
        batch_id: "batch-001",
        report_revision: 10_100,
        comparisons: [{
          baseline,
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
      }),
    });

    render(
      <BatchBaselinePanel
        api={api}
        batch={batchRecord({ status: "completed" })}
        report={batchReport}
      />,
    );

    const user = userEvent.setup();
    await user.click(await screen.findByRole("checkbox", { name: /baseline release.*OFF/i }));

    expect(replaceBaselineSelections).toHaveBeenCalledWith(
      "batch-001",
      [{ baseline_id: "baseline-release", current_arm: "off" }],
      expect.any(AbortSignal),
    );
    expect(await screen.findByText("历史解决率")).toBeVisible();
  });

  it("creates a baseline with the displayed report revision", async () => {
    const createBaseline = vi.fn().mockResolvedValue(baseline);
    const api = apiStub({
      createBaseline,
      getBaselineCandidates: vi.fn().mockResolvedValue([]),
      getBaselineSelections: vi.fn().mockResolvedValue([]),
      getBaselineComparisons: vi.fn().mockResolvedValue({
        batch_id: "batch-001", report_revision: 10_100, comparisons: [],
      }),
    });
    const user = userEvent.setup();

    render(<BatchBaselinePanel api={api} batch={batchRecord({ status: "completed" })} report={batchReport} />);

    await user.type(await screen.findByLabelText("基线名称"), "Release candidate");
    await user.selectOptions(screen.getByLabelText("来源实验臂"), "on");
    await user.click(screen.getByRole("button", { name: "创建基线" }));

    await waitFor(() => {
      expect(createBaseline).toHaveBeenCalledWith(
        {
          name: "Release candidate",
          source_batch_id: "batch-001",
          source_arm: "on",
          expected_report_revision: 10_100,
          idempotency_key: expect.any(String),
        },
        expect.any(AbortSignal),
      );
    });
  });

  it("disables an incompatible candidate and shows the server reason", async () => {
    const incompatibleCandidate = {
      ...baseline,
      baseline_id: "baseline-incompatible",
      name: "Archived baseline",
    };
    const api = apiStub({
      getBaselineCandidates: vi.fn().mockImplementation((_batchID, arm) => Promise.resolve(
        arm === "on"
          ? [{
            baseline: incompatibleCandidate,
            compatibility: { status: "incompatible", reasons: ["模型版本不兼容"] },
          }]
          : [],
      )),
      getBaselineSelections: vi.fn().mockResolvedValue([]),
      getBaselineComparisons: vi.fn().mockResolvedValue({
        batch_id: "batch-001", report_revision: 10_100, comparisons: [],
      }),
    });

    render(<BatchBaselinePanel api={api} batch={batchRecord({ status: "completed" })} report={batchReport} />);

    expect(await screen.findByText("模型版本不兼容")).toBeVisible();
    expect(screen.getByRole("checkbox", { name: /archived baseline.*ON/i })).toBeDisabled();
  });

  it("keeps confirmed selections visible after a selection write fails", async () => {
    const nextBaseline = { ...baseline, baseline_id: "baseline-next", name: "Next baseline" };
    const api = apiStub({
      getBaselineCandidates: vi.fn().mockResolvedValue([
        { baseline, compatibility: { status: "compatible", reasons: [] } },
        { baseline: nextBaseline, compatibility: { status: "compatible", reasons: [] } },
      ]),
      getBaselineSelections: vi.fn().mockResolvedValue([
        { baseline_id: "baseline-release", current_arm: "off" },
      ]),
      replaceBaselineSelections: vi.fn().mockRejectedValue(new Error("write failed")),
      getBaselineComparisons: vi.fn().mockResolvedValue({
        batch_id: "batch-001", report_revision: 10_100, comparisons: [],
      }),
    });
    const user = userEvent.setup();

    render(<BatchBaselinePanel api={api} batch={batchRecord({ status: "completed" })} report={batchReport} />);

    expect(await screen.findByRole("checkbox", { name: /baseline release.*OFF/i })).toBeChecked();
    const next = screen.getByRole("checkbox", { name: /next baseline.*OFF/i });
    await user.click(next);

    expect(await screen.findByRole("alert")).toHaveTextContent("基线选择保存失败");
    expect(screen.getByRole("checkbox", { name: /baseline release.*OFF/i })).toBeChecked();
    expect(next).not.toBeChecked();
  });

  it("does not load baseline state for a batch without paired completed arms", async () => {
    const getBaselineCandidates = vi.fn();
    const api = apiStub({ getBaselineCandidates });

    const { container } = render(
      <BatchBaselinePanel api={api} batch={batchRecord({ status: "running" })} report={batchReport} />,
    );

    await Promise.resolve();
    expect(container).toBeEmptyDOMElement();
    expect(getBaselineCandidates).not.toHaveBeenCalled();
  });

  it("keeps selection controls disabled until the replacement is confirmed", async () => {
    const nextBaseline = { ...baseline, baseline_id: "baseline-next", name: "Next baseline" };
    const confirmedSelections = deferred<BaselineSelection[]>();
    const confirmedComparisons = deferred<BaselineComparisonResponse>();
    const getBaselineSelections = vi.fn()
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(confirmedSelections.promise)
      .mockResolvedValue([{ baseline_id: "baseline-release", current_arm: "off" }]);
    const getBaselineComparisons = vi.fn()
      .mockResolvedValueOnce({ batch_id: "batch-001", report_revision: 10_100, comparisons: [] })
      .mockReturnValueOnce(confirmedComparisons.promise)
      .mockResolvedValue({ batch_id: "batch-001", report_revision: 10_100, comparisons: [] });
    const replaceBaselineSelections = vi.fn().mockResolvedValue([]);
    const api = apiStub({
      getBaselineCandidates: vi.fn().mockImplementation((_batchID, arm) => Promise.resolve(
        arm === "off"
          ? [
            { baseline, compatibility: { status: "compatible", reasons: [] } },
            { baseline: nextBaseline, compatibility: { status: "compatible", reasons: [] } },
          ]
          : [],
      )),
      getBaselineSelections,
      getBaselineComparisons,
      replaceBaselineSelections,
    });
    const user = userEvent.setup();

    render(<BatchBaselinePanel api={api} batch={batchRecord({ status: "completed" })} report={batchReport} />);

    const first = await screen.findByRole("checkbox", { name: /baseline release.*OFF/i });
    const second = screen.getByRole("checkbox", { name: /next baseline.*OFF/i });
    await user.click(first);
    await waitFor(() => expect(getBaselineSelections).toHaveBeenCalledTimes(2));
    await Promise.resolve();

    expect(first).toBeDisabled();
    expect(second).toBeDisabled();

    confirmedSelections.resolve([{ baseline_id: "baseline-release", current_arm: "off" }]);
    confirmedComparisons.resolve({ batch_id: "batch-001", report_revision: 10_100, comparisons: [] });
    await waitFor(() => expect(first).toBeChecked());
    expect(second).toBeEnabled();

    await user.click(second);
    expect(replaceBaselineSelections).toHaveBeenLastCalledWith(
      "batch-001",
      [
        { baseline_id: "baseline-release", current_arm: "off" },
        { baseline_id: "baseline-next", current_arm: "off" },
      ],
      expect.any(AbortSignal),
    );
  });

  it("serializes create and selection actions through confirmed server state", async () => {
    const createResult = deferred<typeof baseline>();
    const confirmedSelections = deferred<[]>();
    const confirmedBaseline = { ...baseline, baseline_id: "baseline-confirmed", name: "Confirmed baseline" };
    const confirmedComparisons = deferred<BaselineComparisonResponse>();
    const getBaselineSelections = vi.fn()
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(confirmedSelections.promise);
    const getBaselineComparisons = vi.fn()
      .mockResolvedValueOnce({ batch_id: "batch-001", report_revision: 10_100, comparisons: [] })
      .mockReturnValueOnce(confirmedComparisons.promise);
    const createBaseline = vi.fn().mockReturnValue(createResult.promise);
    const replaceBaselineSelections = vi.fn();
    const api = apiStub({
      createBaseline,
      getBaselineCandidates: vi.fn().mockResolvedValue([
        { baseline, compatibility: { status: "compatible", reasons: [] } },
      ]),
      getBaselineSelections,
      getBaselineComparisons,
      replaceBaselineSelections,
    });
    const user = userEvent.setup();

    render(<BatchBaselinePanel api={api} batch={batchRecord({ status: "completed" })} report={batchReport} />);

    await user.type(await screen.findByLabelText("基线名称"), "New baseline");
    await user.click(screen.getByRole("button", { name: "创建基线" }));
    const selection = screen.getByRole("checkbox", { name: /baseline release.*OFF/i });

    expect(selection).toBeDisabled();
    expect(screen.getByRole("button", { name: /正在创建|操作进行中/ })).toBeDisabled();
    expect(replaceBaselineSelections).not.toHaveBeenCalled();

    createResult.resolve(baseline);
    await waitFor(() => expect(getBaselineSelections).toHaveBeenCalledTimes(2));
    await Promise.resolve();
    expect(selection).toBeDisabled();
    expect(screen.getByRole("button", { name: /正在创建|操作进行中/ })).toBeDisabled();

    confirmedSelections.resolve([]);
    confirmedComparisons.resolve({
      batch_id: "batch-001",
      report_revision: 10_100,
      comparisons: [{
        baseline: confirmedBaseline,
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
    });

    expect(await screen.findByText("Confirmed baseline")).toBeVisible();
    expect(selection).toBeEnabled();
    expect(screen.getByRole("button", { name: "创建基线" })).toBeEnabled();
    expect(replaceBaselineSelections).not.toHaveBeenCalled();
  });
});
