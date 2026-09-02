# WP5 Single-Arm Execution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox syntax for tracking.

**Goal:** Enable internal OFF-only and ON-only evaluation runs while preserving
the default OFF/ON workflow.

**Architecture:** `TreatmentMode` is the single source of truth for selected
arms. The runner iterates that ordered set, the worker persists only those
outcomes, and report models/loaders require evidence exactly for those arms.

**Tech Stack:** Python 3.11+, Pydantic v2, pytest, FastAPI control-plane
models, SQLite-backed task store, Docker SUT runner.

**Spec:** `docs/superpowers/specs/2026-09-02-wp5-single-arm-execution-design.md`

## Global Constraints

- Keep `off_on` as the default and preserve observable paired behavior.
- Do not modify `evaluation/src/powercontext_eval/web/api.py`, `cli.py`,
  `web/baselines.py`, or the React console; they belong to P2 API/CLI and P3.
- Do not copy unrelated upstream `SutConfig` cleanup from `f4c7ce10`.
- Use real Pydantic objects, retained artifacts, and SQLite-backed tests; mock
  only process, Docker, HTTP, or time boundaries already mocked by the suite.

---

### Task 1: Define Exact Treatment-Mode Report Contracts

**Files:**
- Modify: `evaluation/src/powercontext_eval/models.py`
- Modify: `evaluation/src/powercontext_eval/report.py`
- Modify: `evaluation/src/powercontext_eval/web/batches.py`
- Modify: `evaluation/src/powercontext_eval/web/models.py`
- Test: `evaluation/tests/web/test_reporting.py`

**Interfaces:**
- Produces `TreatmentMode.arms: tuple[Arm, ...]`.
- Produces a `ReportBundle` whose `off` and `on` values are optional only when
  absent from `treatment_mode.arms`.
- Produces nullable `TaskResult.off_resolved` and `TaskResult.on_resolved`.

- [ ] Write a parametrized failing report-loader test for `on_only` and
  `off_only` artifacts containing exactly one evidence directory.
- [ ] Run that test and verify current code rejects the artifact because it
  unconditionally loads both arms.
- [ ] Add `TreatmentMode`, nullable report/result fields, and strict selected
  arm validation; render a single-arm report without a comparison section.
- [ ] Run the report test and its paired-report regression tests.
- [ ] Commit the model/report contract change.

### Task 2: Run Only Requested Arms

**Files:**
- Modify: `evaluation/src/powercontext_eval/runner.py`
- Test: `evaluation/tests/unit/test_runner_phases.py`

**Interfaces:**
- Consumes `RunConfig.treatment_mode: TreatmentMode`.
- Produces `RunResult` with `None` only for unexecuted arms.
- Uses `DockerSut.run_pair` exclusively for `off_on` and `DockerSut.run_arm`
  for `on_only` or `off_only`.

- [ ] Write a parametrized failing runner test for `on_only` and `off_only`
  that asserts one SUT arm, one official evaluation, one report arm, and an
  absent unexecuted result.
- [ ] Run the new test and verify current code invokes both arms.
- [ ] Add `RunConfig.treatment_mode`, select `TreatmentMode.arms`, and build
  paths, stores, context traces, official evaluations, and report arms only
  for that set.
- [ ] Run focused runner phase tests and the existing paired runner tests.
- [ ] Commit the runner change.

### Task 3: Persist and Validate Selected Outcomes in the Worker

**Files:**
- Modify: `evaluation/src/powercontext_eval/web/worker.py`
- Test: `evaluation/tests/web/test_worker.py`

**Interfaces:**
- Consumes `TaskRecord.request.treatment_mode`.
- Passes that mode to `RunConfig`.
- Rejects a runner result whose non-null outcome arms differ from the requested
  set before `TaskStore.succeed` persists it.

- [ ] Write a failing worker test for an `on_only` task whose runner returns an
  OFF outcome, asserting safe failure instead of success.
- [ ] Run it and verify the current worker accepts the mismatched result.
- [ ] Pass the task mode into `_batch_run_config`; compare non-null runner
  result arms with `task.request.treatment_mode.arms` in `_validated_result`.
- [ ] Run focused worker tests, including existing paired success and retry
  cases.
- [ ] Commit the worker change.

### Task 4: Validate the Complete P2a Contract

**Files:**
- Test: `evaluation/tests/unit/test_runner_phases.py`
- Test: `evaluation/tests/web/test_reporting.py`
- Test: `evaluation/tests/web/test_worker.py`

- [ ] Run the three focused test files with the project Linux runtime.
- [ ] Run `ruff check evaluation`, `ruff format --check evaluation`, and
  `ty check src`.
- [ ] Run the evaluation control-plane test suite or record any reproduced
  environment-only gap against an unmodified baseline.
- [ ] Inspect `git diff --check` and the exact changed-file inventory.
- [ ] Commit only P2a files; push, open the P2a PR, and verify exact-Head and
  post-main CI before updating Issue #109.
