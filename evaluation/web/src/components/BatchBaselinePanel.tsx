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

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";

import type { EvaluationApi } from "../api";
import type {
  BaselineCandidate,
  BaselineComparison,
  BaselineComparisonResponse,
  BaselineSelection,
  BatchRecord,
  BatchReport,
} from "../types";

type CurrentArm = "off" | "on";

interface BatchBaselinePanelProps {
  api: EvaluationApi;
  batch: BatchRecord;
  report: BatchReport;
}

interface BaselinePanelState {
  offCandidates: BaselineCandidate[];
  onCandidates: BaselineCandidate[];
  selections: BaselineSelection[];
  comparisons: BaselineComparisonResponse;
}

type LoadOutcome = "confirmed" | "failed" | "stale";

const currentArms: CurrentArm[] = ["off", "on"];

function emptyPanelState(batchId: string, reportRevision: number): BaselinePanelState {
  return {
    offCandidates: [],
    onCandidates: [],
    selections: [],
    comparisons: { batch_id: batchId, report_revision: reportRevision, comparisons: [] },
  };
}

function isSelected(selections: BaselineSelection[], baselineID: string, currentArm: CurrentArm): boolean {
  return selections.some(
    (selection) => selection.baseline_id === baselineID && selection.current_arm === currentArm,
  );
}

export function BatchBaselinePanel({ api, batch, report }: BatchBaselinePanelProps) {
  const pairedCompletedBatch = batch.status === "completed" && batch.request.treatment_mode === "off_on";
  const [state, setState] = useState(() => emptyPanelState(batch.batch_id, report.report_revision));
  const [loadError, setLoadError] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [mutationInFlight, setMutationInFlight] = useState(false);
  const [name, setName] = useState("");
  const [sourceArm, setSourceArm] = useState<CurrentArm>("off");
  const readController = useRef<AbortController | null>(null);
  const actionController = useRef<AbortController | null>(null);
  const readGeneration = useRef(0);
  const actionGeneration = useRef(0);
  const mutationActive = useRef(false);

  const load = useCallback(async (): Promise<LoadOutcome> => {
    if (!pairedCompletedBatch) return "stale";

    readController.current?.abort();
    const controller = new AbortController();
    readController.current = controller;
    const generation = ++readGeneration.current;
    setLoadError(false);

    try {
      const [offCandidates, onCandidates, selections, comparisons] = await Promise.all([
        api.getBaselineCandidates(batch.batch_id, "off", controller.signal),
        api.getBaselineCandidates(batch.batch_id, "on", controller.signal),
        api.getBaselineSelections(batch.batch_id, controller.signal),
        api.getBaselineComparisons(batch.batch_id, controller.signal),
      ]);
      if (controller.signal.aborted || generation !== readGeneration.current) return "stale";
      setState({ offCandidates, onCandidates, selections, comparisons });
      return "confirmed";
    } catch {
      if (!controller.signal.aborted && generation === readGeneration.current) {
        setLoadError(true);
        return "failed";
      }
      return "stale";
    }
  }, [api, batch.batch_id, pairedCompletedBatch]);

  useEffect(() => {
    void load();
    return () => {
      readController.current?.abort();
      actionController.current?.abort();
      readGeneration.current += 1;
      actionGeneration.current += 1;
    };
  }, [load]);

  const runMutation = (
    operation: (signal: AbortSignal) => Promise<unknown>,
    failureMessage: string,
    afterConfirmation?: () => void,
  ) => {
    if (mutationActive.current) return;

    mutationActive.current = true;
    const controller = new AbortController();
    actionController.current = controller;
    const generation = ++actionGeneration.current;
    setMutationInFlight(true);
    setActionError(null);

    void (async () => {
      try {
        await operation(controller.signal);
        if (controller.signal.aborted || generation !== actionGeneration.current) return;

        const outcome = await load();
        if (outcome === "confirmed") afterConfirmation?.();
      } catch {
        if (!controller.signal.aborted && generation === actionGeneration.current) {
          setActionError(failureMessage);
        }
      } finally {
        if (!controller.signal.aborted && generation === actionGeneration.current) {
          actionController.current = null;
          mutationActive.current = false;
          setMutationInFlight(false);
        }
      }
    })();
  };

  const createBaseline = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!pairedCompletedBatch || !name.trim() || mutationActive.current) return;

    runMutation(
      (signal) => api.createBaseline({
        name: name.trim(),
        source_batch_id: batch.batch_id,
        source_arm: sourceArm,
        expected_report_revision: report.report_revision,
        idempotency_key: crypto.randomUUID(),
      }, signal),
      "基线创建失败，已保留当前已确认数据。",
      () => setName(""),
    );
  };

  const replaceSelection = (baselineID: string, currentArm: CurrentArm) => {
    if (!pairedCompletedBatch || mutationActive.current) return;

    const selected = isSelected(state.selections, baselineID, currentArm);
    const nextSelections = selected
      ? state.selections.filter(
        (selection) => selection.baseline_id !== baselineID || selection.current_arm !== currentArm,
      )
      : [...state.selections, { baseline_id: baselineID, current_arm: currentArm }];
    runMutation(
      (signal) => api.replaceBaselineSelections(batch.batch_id, nextSelections, signal),
      "基线选择保存失败，已保留当前已确认数据。",
    );
  };

  if (!pairedCompletedBatch) return null;

  if (loadError) {
    return (
      <section className="report-section report-section--batch baseline-panel">
        <h2>当前批次与基线</h2>
        <p className="error-message">基线数据暂时无法加载。</p>
        <button type="button" className="secondary-button" onClick={load}>重试</button>
      </section>
    );
  }

  return (
    <section className="report-section report-section--batch baseline-panel" aria-labelledby="baseline-panel-heading">
      <div className="section-heading">
        <div>
          <h2 id="baseline-panel-heading">当前批次与基线</h2>
          <p>候选兼容性、已选基线和历史对比均来自服务端。</p>
        </div>
      </div>

      <form className="baseline-create-form" onSubmit={createBaseline}>
        <label>
          基线名称
          <input value={name} onChange={(event) => setName(event.target.value)} required />
        </label>
        <label>
          来源实验臂
          <select value={sourceArm} onChange={(event) => setSourceArm(event.target.value as CurrentArm)}>
            <option value="off">OFF</option>
            <option value="on">ON</option>
          </select>
        </label>
        <button type="submit" className="primary-button" disabled={mutationInFlight}>
          {mutationInFlight ? "操作进行中…" : "创建基线"}
        </button>
      </form>

      {actionError !== null && <p className="form-feedback error-message" role="alert">{actionError}</p>}

      <div className="baseline-candidate-grid">
        {currentArms.map((currentArm) => (
          <CandidateTable
            key={currentArm}
            currentArm={currentArm}
            candidates={currentArm === "off" ? state.offCandidates : state.onCandidates}
            selections={state.selections}
            disabled={mutationInFlight}
            onToggle={replaceSelection}
          />
        ))}
      </div>

      <ComparisonTable comparisons={state.comparisons.comparisons} />
    </section>
  );
}

function CandidateTable({
  currentArm,
  candidates,
  selections,
  disabled,
  onToggle,
}: {
  currentArm: CurrentArm;
  candidates: BaselineCandidate[];
  selections: BaselineSelection[];
  disabled: boolean;
  onToggle(baselineID: string, currentArm: CurrentArm): void;
}) {
  const armLabel = currentArm.toUpperCase();
  return (
    <div className="baseline-candidates">
      <h3>{armLabel} 当前实验臂</h3>
      {candidates.length === 0 ? (
        <p className="state-message">没有可用基线。</p>
      ) : (
        <div className="table-scroll">
          <table className="baseline-table">
            <thead>
              <tr><th scope="col">选择</th><th scope="col">基线</th><th scope="col">兼容性</th></tr>
            </thead>
            <tbody>
              {candidates.map(({ baseline, compatibility }) => {
                const incompatible = compatibility.status === "incompatible";
                const label = `${baseline.name} ${armLabel}`;
                return (
                  <tr key={baseline.baseline_id}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={label}
                        checked={isSelected(selections, baseline.baseline_id, currentArm)}
                        disabled={disabled || incompatible}
                        onChange={() => onToggle(baseline.baseline_id, currentArm)}
                      />
                    </td>
                    <th scope="row">{baseline.name}</th>
                    <td>
                      <span className={`baseline-compatibility baseline-compatibility--${compatibility.status}`}>
                        {compatibility.status === "compatible" ? "兼容" : compatibility.status === "warning" ? "兼容性警告" : "不兼容"}
                      </span>
                      {compatibility.reasons.map((reason) => <div className="baseline-reason" key={reason}>{reason}</div>)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function ComparisonTable({ comparisons }: { comparisons: BaselineComparison[] }) {
  return (
    <div className="baseline-comparisons">
      <h3>历史解决率</h3>
      {comparisons.length === 0 ? (
        <p className="state-message">尚未选择可比较的基线。</p>
      ) : (
        <div className="table-scroll">
          <table className="baseline-table baseline-comparison-table">
            <thead>
              <tr>
                <th scope="col">基线</th>
                <th scope="col">当前臂</th>
                <th scope="col">任务覆盖</th>
                <th scope="col">基线解决率</th>
                <th scope="col">当前解决率</th>
                <th scope="col">差值</th>
              </tr>
            </thead>
            <tbody>
              {comparisons.map((comparison) => (
                <tr key={`${comparison.baseline.baseline_id}:${comparison.current_arm}`}>
                  <th scope="row">{comparison.baseline.name}</th>
                  <td>{comparison.current_arm.toUpperCase()}</td>
                  <td>{comparison.coverage.matched_tasks} / {comparison.coverage.comparable_tasks}</td>
                  <td>{comparison.resolution.baseline_rate_percent}%</td>
                  <td>{comparison.resolution.current_rate_percent}%</td>
                  <td>{comparison.resolution.delta_points} pp</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
