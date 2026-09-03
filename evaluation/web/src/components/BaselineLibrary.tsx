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

import { useCallback, useEffect, useRef, useState, type MouseEvent } from "react";

import type { EvaluationApi } from "../api";
import type { BaselineRecord } from "../types";

export function BaselineLibrary({ api, navigate }: { api: EvaluationApi; navigate(path: string): void }) {
  const [baselines, setBaselines] = useState<BaselineRecord[] | null>(null);
  const [error, setError] = useState(false);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const load = useCallback(() => {
    controller.current?.abort();
    const nextController = new AbortController();
    controller.current = nextController;
    const currentGeneration = ++generation.current;
    setBaselines(null);
    setError(false);
    api.listBaselines(nextController.signal)
      .then((nextBaselines) => {
        if (!nextController.signal.aborted && currentGeneration === generation.current) {
          setBaselines(nextBaselines);
        }
      })
      .catch(() => {
        if (!nextController.signal.aborted && currentGeneration === generation.current) setError(true);
      });
  }, [api]);

  useEffect(() => {
    load();
    return () => {
      controller.current?.abort();
      generation.current += 1;
    };
  }, [load]);

  const onLink = (event: MouseEvent<HTMLAnchorElement>, path: string) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(path);
  };

  return (
    <section className="baseline-library" aria-labelledby="baseline-library-heading">
      <header className="page-header">
        <p className="eyebrow">PowerContext Evaluation</p>
        <h1 id="baseline-library-heading">基线库</h1>
        <p>查看不可变基线及其来源评测报告。</p>
      </header>
      {error ? (
        <section className="panel empty-state">
          <p>基线暂时无法加载。</p>
          <button type="button" className="secondary-button" onClick={load}>重试</button>
        </section>
      ) : baselines === null ? (
        <section className="panel state-message">正在加载基线…</section>
      ) : baselines.length === 0 ? (
        <section className="panel empty-state">暂无基线。</section>
      ) : (
        <section className="baseline-library__table-wrap" aria-label="基线列表">
          <table className="baseline-library__table">
            <thead>
              <tr>
                <th scope="col">名称</th>
                <th scope="col">来源</th>
                <th scope="col">解决任务</th>
                <th scope="col">执行失败</th>
                <th scope="col">模型</th>
                <th scope="col">创建时间</th>
              </tr>
            </thead>
            <tbody>
              {baselines.map((baseline) => {
                const sourceReportPath = `/report/${encodeURIComponent(baseline.source_batch_id)}`;
                return (
                  <tr key={baseline.baseline_id}>
                    <th scope="row">
                      <strong>{baseline.name}</strong>
                      <span>{baseline.source_arm.toUpperCase()} · 修订 {baseline.source_report_revision}</span>
                    </th>
                    <td>
                      <a href={sourceReportPath} onClick={(event) => onLink(event, sourceReportPath)}>
                        查看总体报告
                      </a>
                    </td>
                    <td>{baseline.resolved_tasks} / {baseline.total_tasks}</td>
                    <td>{baseline.execution_failures}</td>
                    <td>{baseline.model}</td>
                    <td>{new Date(baseline.created_at).toLocaleString("zh-CN", { timeZone: "UTC" })}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}
    </section>
  );
}
