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

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { apiStub } from "../test/fixtures";
import type { BaselineRecord } from "../types";
import { BaselineLibrary } from "./BaselineLibrary";

const baseline: BaselineRecord = {
  baseline_id: "baseline/release",
  name: "Release candidate",
  source_batch_id: "batch/source",
  source_arm: "on",
  source_report_revision: 217,
  benchmark: "swebench-pro",
  task_set: "swebench-pro-public-v2",
  instance_set_digest: "a".repeat(64),
  total_tasks: 731,
  resolved_tasks: 412,
  execution_failures: 3,
  model: "gpt-5.6-sol",
  reasoning_effort: "medium",
  dataset_revision: "public-v2",
  harness_revision: "harness-sha",
  powercontext_sha: "b".repeat(40),
  codex_version: "1.0.0",
  created_at: "2026-09-02T00:00:00Z",
};

describe("BaselineLibrary", () => {
  it("shows immutable baseline facts and routes an ordinary source-report click internally", async () => {
    const navigate = vi.fn();
    const api = apiStub({ listBaselines: vi.fn().mockResolvedValue([baseline]) });
    const user = userEvent.setup();
    render(<BaselineLibrary api={api} navigate={navigate} />);

    expect(await screen.findByRole("heading", { name: "基线库" })).toBeVisible();
    expect(screen.getByText("Release candidate")).toBeVisible();
    expect(screen.getByText("412 / 731")).toBeVisible();
    const sourceReport = screen.getByRole("link", { name: "查看总体报告" });
    expect(sourceReport).toHaveAttribute("href", "/report/batch%2Fsource");

    await user.click(sourceReport);
    expect(navigate).toHaveBeenCalledWith("/report/batch%2Fsource");
  });

  it("preserves modifier-click handling for a source report link", async () => {
    const navigate = vi.fn();
    render(<BaselineLibrary api={apiStub({ listBaselines: vi.fn().mockResolvedValue([baseline]) })} navigate={navigate} />);

    const sourceReport = await screen.findByRole("link", { name: "查看总体报告" });
    fireEvent.click(sourceReport, { ctrlKey: true });

    expect(navigate).not.toHaveBeenCalled();
  });

  it("leaves an empty response distinguishable from a loading or error state", async () => {
    render(<BaselineLibrary api={apiStub({ listBaselines: vi.fn().mockResolvedValue([]) })} navigate={vi.fn()} />);

    expect(await screen.findByText("暂无基线。")).toBeVisible();
  });

  it("offers a retry after the baseline list fails", async () => {
    const listBaselines = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce([baseline]);
    const user = userEvent.setup();
    render(<BaselineLibrary api={apiStub({ listBaselines })} navigate={vi.fn()} />);

    expect(await screen.findByText("基线暂时无法加载。")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("Release candidate")).toBeVisible();
  });
});
