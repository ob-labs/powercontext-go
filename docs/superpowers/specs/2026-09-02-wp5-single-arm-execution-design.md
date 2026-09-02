# WP5 Single-Arm Execution Design

## Goal

Complete the internal execution portion of WP5 P2 so an evaluation task can
run `off_on`, `off_only`, or `on_only` without inventing an unexecuted arm.

## Scope

This slice owns the evaluation domain, runner, worker, retained report, and
batch model paths. It does not modify the evaluation HTTP API, CLI commands,
Baseline storage contracts, React console, generated files, or Go product
surface. PR #167 remains the independent API/CLI slice; the Baseline Library
remains P3 work.

## Contract

- `TreatmentMode` declares the exact ordered arm set: `off_on`, `on_only`, or
  `off_only`.
- A single-arm run creates workspaces, evidence, official evaluation output,
  context trace, report data, and result state only for its selected arm.
- `ReportBundle` and the web report projection reject a missing required arm or
  an extra unrequested arm. OFF/ON comparison data is present only for
  `off_on`.
- A completed task result has `None` for the unexecuted arm. The worker checks
  that returned arm outcomes exactly match the requested mode before persisting
  success.
- The default remains `off_on`; all paired behavior, paired acceptance rules,
  and existing public API response shapes remain compatible.

## Error Handling

- A report with missing, malformed, or extra treatment evidence is rejected as
  an invalid retained report.
- A runner outcome whose populated arms differ from the requested mode is an
  invalid report bundle and fails the claimed task through the existing safe
  worker failure path.
- A single-arm report has no OFF/ON comparison. It does not synthesize zeroes,
  failures, treatment evidence, or metrics for the absent arm.

## File Boundaries

- `evaluation/src/powercontext_eval/models.py`: `TreatmentMode` value object.
- `evaluation/src/powercontext_eval/report.py`: retained report arm invariants
  and comparison rendering.
- `evaluation/src/powercontext_eval/runner.py`: arm selection, run result, and
  persisted report construction.
- `evaluation/src/powercontext_eval/web/batches.py` and `web/models.py`:
  task/batch treatment mode and nullable unexecuted results.
- `evaluation/src/powercontext_eval/web/worker.py`: pass the selected mode and
  validate exact returned outcomes.
- `evaluation/src/powercontext_eval/web/reporting.py`: load only selected-arm
  evidence and project an absent arm as `null`.
- Adjacent unit and web tests prove the execution and retained-artifact
  contracts with real model and filesystem state.

## Acceptance

1. Tests prove `on_only` and `off_only` run exactly one requested arm and
   persist only that arm's report/outcome.
2. Tests prove the web loader accepts a valid single-arm artifact and rejects
   missing or extra evidence for its declared mode.
3. Tests prove the worker refuses a runner result that reports an unrequested
   arm and preserves the existing `off_on` flow.
4. Focused Python tests, lint, format, and type checks pass on Linux; the full
   evaluation control-plane CI remains the final delivery signal.
