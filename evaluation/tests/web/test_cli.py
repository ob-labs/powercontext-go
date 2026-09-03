# Copyright (c) 2026 OceanBase.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from __future__ import annotations

import json
import signal
import subprocess
import sys
import textwrap
from pathlib import Path
from typing import Any, Self

import pytest
from typer.testing import CliRunner

from powercontext_eval.cli import _request_worker_stop, app
from powercontext_eval.web.config import WebConfig


class _JSONResponse:
    def __init__(self, payload: object) -> None:
        self._payload = payload

    def __enter__(self) -> Self:
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def read(self) -> bytes:
        return json.dumps(self._payload).encode("utf-8")


def test_top_level_help_exposes_service_commands() -> None:
    result = CliRunner().invoke(app, ["--help"])

    assert result.exit_code == 0
    assert "web" in result.output
    assert "worker" in result.output


def test_web_builds_config_from_cli_root_and_environment(monkeypatch, tmp_path: Path) -> None:
    calls: dict[str, object] = {}
    application = object()

    monkeypatch.setenv("POWERCONTEXT_EVAL_PORT", "8123")
    monkeypatch.setenv("POWERCONTEXT_EVAL_HOST", "127.0.0.2")
    monkeypatch.setenv("POWERCONTEXT_EVAL_TOKENSFLOW_EGRESS_NETWORK", "tokensflow-egress")
    monkeypatch.setenv("POWERCONTEXT_EVAL_PROXY_URL", "http://127.0.0.1:8081")

    def fake_create_app(config: object) -> object:
        calls["config"] = config
        return application

    monkeypatch.setattr("powercontext_eval.web.api.create_app", fake_create_app)

    def fake_run(app: object, *, host: str, port: int) -> None:
        calls.update(app=app, host=host, port=port)

    monkeypatch.setattr("uvicorn.run", fake_run)

    result = CliRunner().invoke(app, ["web", "--root", str(tmp_path)])

    assert result.exit_code == 0, result.output
    config = calls["config"]
    assert isinstance(config, WebConfig)
    assert config.root == tmp_path
    assert calls == {"config": config, "app": application, "host": "127.0.0.2", "port": 8123}


def test_worker_initializes_store_and_runs_with_configured_poll(monkeypatch, tmp_path: Path) -> None:
    calls: list[tuple[object, ...]] = []

    class FakeStore:
        def __init__(self, database: Path, *, lease_duration: object, max_attempts: int) -> None:
            calls.append(("store", database, lease_duration, max_attempts))

        def initialize(self) -> None:
            calls.append(("initialize",))

    class FakeWorker:
        def __init__(self, config: object, store: object, *, usage_probe: object) -> None:
            calls.append(("worker", config, store, usage_probe))

        def run_forever(self) -> None:
            calls.append(("run_forever",))

        def stop(self) -> None:
            calls.append(("stop",))

    monkeypatch.setenv("POWERCONTEXT_EVAL_POLL_SECONDS", "2.5")
    monkeypatch.setenv("POWERCONTEXT_EVAL_LEASE_SECONDS", "90")
    monkeypatch.setenv("POWERCONTEXT_EVAL_TOKENSFLOW_EGRESS_NETWORK", "tokensflow-egress")
    monkeypatch.setenv("POWERCONTEXT_EVAL_PROXY_URL", "http://127.0.0.1:8081")
    monkeypatch.setattr("powercontext_eval.web.store.TaskStore", FakeStore)
    monkeypatch.setattr("powercontext_eval.web.worker.EvaluationWorker", FakeWorker)
    monkeypatch.setattr("signal.getsignal", lambda _signal: signal.SIG_DFL)
    monkeypatch.setattr("signal.signal", lambda *args: calls.append(("signal", *args)))

    result = CliRunner().invoke(app, ["worker", "--root", str(tmp_path)])

    assert result.exit_code == 0, result.output
    assert calls[0][0] == "store"
    assert calls[0][3] == 5
    assert calls[1] == ("initialize",)
    assert calls[2][0] == "worker"
    assert isinstance(calls[2][1], WebConfig)
    assert ("run_forever",) in calls
    assert calls[2][1].poll_seconds == 2.5
    assert ("stop",) not in calls


def test_signal_callback_requests_graceful_worker_stop() -> None:
    calls: list[str] = []

    class Worker:
        def stop(self) -> None:
            calls.append("stop")

    _request_worker_stop(Worker(), signal.SIGTERM, None)

    assert calls == ["stop"]


def test_worker_signal_handler_ignores_reentrant_sigterm_while_first_stop_is_running() -> None:
    program = textwrap.dedent(
        """
        import os
        import signal
        import threading

        from powercontext_eval.cli import _worker_signal_handlers


        class Worker:
            def __init__(self) -> None:
                self.calls = 0
                self.lock = threading.Lock()
                self.first_stop_entered = threading.Event()
                self.second_signal_sent = threading.Event()

            def stop(self) -> None:
                self.calls += 1
                with self.lock:
                    self.first_stop_entered.set()
                    assert self.second_signal_sent.wait(timeout=1)


        worker = Worker()


        def send_second_signal() -> None:
            assert worker.first_stop_entered.wait(timeout=1)
            os.kill(os.getpid(), signal.SIGTERM)
            worker.second_signal_sent.set()


        sender = threading.Thread(target=send_second_signal, daemon=True)
        with _worker_signal_handlers(worker):
            sender.start()
            os.kill(os.getpid(), signal.SIGTERM)
        sender.join(timeout=1)
        print(f"stops={worker.calls}")
        """
    )

    completed = subprocess.run(
        (sys.executable, "-c", program),
        capture_output=True,
        text=True,
        timeout=5,
        check=False,
    )

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout == "stops=1\n"


def test_invalid_configuration_is_concise_and_does_not_print_secrets(monkeypatch) -> None:
    secret = "https://user:secret@proxy.invalid"
    monkeypatch.setenv("POWERCONTEXT_EVAL_ROOT", "relative")
    monkeypatch.setenv("POWERCONTEXT_EVAL_PROXY_URL", secret)

    result = CliRunner().invoke(app, ["web"])

    assert result.exit_code == 2
    assert "Invalid evaluation configuration" in result.output
    assert secret not in result.output
    assert "validation error" not in result.output.casefold()


def test_baseline_help_exposes_subcommands() -> None:
    result = CliRunner().invoke(app, ["baseline", "--help"], color=False)

    assert result.exit_code == 0
    assert "create" in result.output
    assert "list" in result.output
    assert "get" in result.output
    assert "items" in result.output
    assert "candidates" in result.output
    assert "select" in result.output
    assert "compare" in result.output


@pytest.mark.parametrize(
    ("arguments", "expected_url", "response"),
    (
        (("list",), "https://console.example/api/baselines", []),
        (("get", "baseline/alpha"), "https://console.example/api/baselines/baseline%2Falpha", {}),
        (("items", "baseline/alpha"), "https://console.example/api/baselines/baseline%2Falpha/items", []),
        (
            ("candidates", "batch/alpha", "on"),
            "https://console.example/api/batches/batch%2Falpha/baseline-candidates?current_arm=on",
            [],
        ),
        (("compare", "batch/alpha"), "https://console.example/api/batches/batch%2Falpha/baseline-comparisons", {}),
    ),
)
def test_baseline_read_commands_request_the_expected_resource(
    monkeypatch: pytest.MonkeyPatch,
    arguments: tuple[str, ...],
    expected_url: str,
    response: object,
) -> None:
    requests: list[Any] = []

    def urlopen(request: Any, *, timeout: float) -> _JSONResponse:
        assert timeout == 30
        requests.append(request)
        return _JSONResponse(response)

    monkeypatch.setattr("powercontext_eval.cli.urlopen", urlopen)

    result = CliRunner().invoke(
        app,
        ["baseline", *arguments, "--console-url", "https://console.example"],
    )

    assert result.exit_code == 0, result.output
    assert [(request.get_method(), request.full_url) for request in requests] == [("GET", expected_url)]
    assert json.loads(result.output) == response


def test_baseline_create_fetches_the_current_report_revision_when_omitted(monkeypatch) -> None:
    requests: list[Any] = []
    responses = iter(({"report_revision": 217}, {"baseline_id": "baseline-217"}))

    def urlopen(request: Any, *, timeout: float) -> _JSONResponse:
        assert timeout == 30
        requests.append(request)
        return _JSONResponse(next(responses))

    monkeypatch.setattr("powercontext_eval.cli.urlopen", urlopen)

    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "create",
            "--console-url",
            "https://console.example",
            "--source-batch-id",
            "batch/alpha",
            "--source-arm",
            "on",
            "--name",
            "Release candidate",
            "--idempotency-key",
            "baseline-create-217",
        ],
    )

    assert result.exit_code == 0, result.output
    assert [(request.get_method(), request.full_url) for request in requests] == [
        ("GET", "https://console.example/api/batches/batch%2Falpha/report"),
        ("POST", "https://console.example/api/baselines"),
    ]
    assert json.loads(requests[1].data) == {
        "name": "Release candidate",
        "source_batch_id": "batch/alpha",
        "source_arm": "on",
        "expected_report_revision": 217,
        "idempotency_key": "baseline-create-217",
    }
    assert json.loads(result.output) == {"baseline_id": "baseline-217"}


def test_baseline_select_preserves_existing_selections_when_adding_one(monkeypatch) -> None:
    requests: list[Any] = []
    existing = [{"baseline_id": "baseline-existing", "current_arm": "off"}]
    responses = iter((existing, [*existing, {"baseline_id": "baseline-new", "current_arm": "on"}]))

    def urlopen(request: Any, *, timeout: float) -> _JSONResponse:
        assert timeout == 30
        requests.append(request)
        return _JSONResponse(next(responses))

    monkeypatch.setattr("powercontext_eval.cli.urlopen", urlopen)

    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "select",
            "batch/alpha",
            "--console-url",
            "https://console.example",
            "--baseline-id",
            "baseline-new",
            "--current-arm",
            "on",
        ],
    )

    assert result.exit_code == 0, result.output
    assert [(request.get_method(), request.full_url) for request in requests] == [
        ("GET", "https://console.example/api/batches/batch%2Falpha/baseline-selections"),
        ("PUT", "https://console.example/api/batches/batch%2Falpha/baseline-selections"),
    ]
    assert json.loads(requests[1].data) == {
        "selections": [
            {"baseline_id": "baseline-existing", "current_arm": "off"},
            {"baseline_id": "baseline-new", "current_arm": "on"},
        ]
    }
    assert json.loads(result.output) == [
        {"baseline_id": "baseline-existing", "current_arm": "off"},
        {"baseline_id": "baseline-new", "current_arm": "on"},
    ]


# --- Additional baseline CLI test coverage ---


def test_baseline_create_sends_post_and_echoes_result(monkeypatch) -> None:
    baseline_response = {
        "baseline_id": "baseline-20260902-001",
        "name": "my-baseline",
        "source_batch_id": "batch-123",
        "source_arm": "off",
    }
    calls: list[tuple[str, str, bytes | None]] = []

    def fake_urlopen(request, *, timeout=30):
        calls.append((request.method, request.full_url, request.data))
        return _JSONResponse(baseline_response)

    monkeypatch.setattr("powercontext_eval.cli.urlopen", fake_urlopen)

    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "create",
            "--source-batch-id",
            "batch-123",
            "--source-arm",
            "off",
            "--name",
            "my-baseline",
            "--idempotency-key",
            "test-key-001",
            "--expected-report-revision",
            "42",
        ],
    )

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert output["baseline_id"] == "baseline-20260902-001"
    assert len(calls) == 1
    assert calls[0][0] == "POST"
    assert "/api/baselines" in calls[0][1]
    body = json.loads(calls[0][2])
    assert body["name"] == "my-baseline"
    assert body["source_batch_id"] == "batch-123"
    assert body["source_arm"] == "off"
    assert body["expected_report_revision"] == 42
    assert body["idempotency_key"] == "test-key-001"


def test_baseline_list_echoes_json_array(monkeypatch) -> None:
    baselines = [
        {"baseline_id": "b-1", "name": "first"},
        {"baseline_id": "b-2", "name": "second"},
    ]

    monkeypatch.setattr("powercontext_eval.cli.urlopen", lambda req, **kw: _JSONResponse(baselines))

    result = CliRunner().invoke(app, ["baseline", "list"])

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert len(output) == 2
    assert output[0]["name"] == "first"


def test_baseline_get_echoes_baseline_record(monkeypatch) -> None:
    baseline = {"baseline_id": "baseline-get-001", "name": "get-test"}

    monkeypatch.setattr("powercontext_eval.cli.urlopen", lambda req, **kw: _JSONResponse(baseline))

    result = CliRunner().invoke(app, ["baseline", "get", "baseline-get-001"])

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert output["baseline_id"] == "baseline-get-001"


def test_baseline_items_echoes_item_list(monkeypatch) -> None:
    items = [
        {"baseline_id": "b-1", "instance_id": "inst-a", "status": "succeeded"},
        {"baseline_id": "b-1", "instance_id": "inst-b", "status": "failed"},
    ]

    monkeypatch.setattr("powercontext_eval.cli.urlopen", lambda req, **kw: _JSONResponse(items))

    result = CliRunner().invoke(app, ["baseline", "items", "b-1"])

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert len(output) == 2


def test_baseline_candidates_echoes_compatibility(monkeypatch) -> None:
    candidates = [
        {
            "baseline": {"baseline_id": "b-1", "name": "test"},
            "compatibility": {"status": "compatible", "reasons": []},
        }
    ]

    monkeypatch.setattr("powercontext_eval.cli.urlopen", lambda req, **kw: _JSONResponse(candidates))

    result = CliRunner().invoke(app, ["baseline", "candidates", "batch-123", "off"])

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert output[0]["compatibility"]["status"] == "compatible"


def test_baseline_select_deduplicates_by_baseline_id_and_arm(monkeypatch) -> None:
    existing = [{"baseline_id": "b-1", "current_arm": "off"}]
    put_payloads: list[dict] = []

    def fake_urlopen(request, *, timeout=30):
        if request.method == "GET":
            return _JSONResponse(existing)
        put_payloads.append(json.loads(request.data))
        return _JSONResponse([{"baseline_id": "b-1", "current_arm": "off"}])

    monkeypatch.setattr("powercontext_eval.cli.urlopen", fake_urlopen)

    result = CliRunner().invoke(
        app,
        ["baseline", "select", "batch-123", "--baseline-id", "b-1", "--current-arm", "off"],
    )

    assert result.exit_code == 0, result.output
    assert len(put_payloads) == 1
    assert len(put_payloads[0]["selections"]) == 1


def test_baseline_compare_echoes_comparison_result(monkeypatch) -> None:
    comparison = {
        "batch_id": "batch-123",
        "report_revision": 5,
        "comparisons": [
            {
                "baseline": {"baseline_id": "b-1"},
                "current_arm": "off",
                "compatibility": {"status": "compatible"},
                "resolution": {"delta_points": 5.0},
            }
        ],
    }

    monkeypatch.setattr("powercontext_eval.cli.urlopen", lambda req, **kw: _JSONResponse(comparison))

    result = CliRunner().invoke(app, ["baseline", "compare", "batch-123"])

    assert result.exit_code == 0, result.output
    output = json.loads(result.output)
    assert output["batch_id"] == "batch-123"
    assert len(output["comparisons"]) == 1


def test_baseline_create_handles_404(monkeypatch) -> None:
    from urllib.error import HTTPError

    def fake_urlopen(request, *, timeout=30):
        raise HTTPError(request.full_url, 404, "Not Found", {}, None)

    monkeypatch.setattr("powercontext_eval.cli.urlopen", fake_urlopen)

    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "create",
            "--source-batch-id",
            "missing-batch",
            "--source-arm",
            "off",
            "--name",
            "test",
            "--idempotency-key",
            "err-key-001",
            "--expected-report-revision",
            "0",
        ],
    )

    assert result.exit_code != 0
    assert "not found" in result.output.casefold()


def test_baseline_create_handles_409(monkeypatch) -> None:
    from io import BytesIO
    from urllib.error import HTTPError

    def fake_urlopen(request, *, timeout=30):
        raise HTTPError(request.full_url, 409, "Conflict", {}, BytesIO(b"report_revision_conflict"))

    monkeypatch.setattr("powercontext_eval.cli.urlopen", fake_urlopen)

    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "create",
            "--source-batch-id",
            "batch-123",
            "--source-arm",
            "off",
            "--name",
            "test",
            "--idempotency-key",
            "err-key-002",
            "--expected-report-revision",
            "0",
        ],
    )

    assert result.exit_code != 0
    assert "conflict" in result.output.casefold()


def test_baseline_create_rejects_invalid_console_url(monkeypatch) -> None:
    result = CliRunner().invoke(
        app,
        [
            "baseline",
            "create",
            "--console-url",
            "ftp://invalid",
            "--source-batch-id",
            "batch-123",
            "--source-arm",
            "off",
            "--name",
            "test",
            "--idempotency-key",
            "url-err-key-001",
            "--expected-report-revision",
            "0",
        ],
    )

    assert result.exit_code != 0
    assert "invalid" in result.output.casefold()
