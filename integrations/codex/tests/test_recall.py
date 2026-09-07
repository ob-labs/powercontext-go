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

from hashlib import sha256
import io
import json
import os
import stat
import sys
import threading
import time
from collections.abc import Iterator
from contextlib import contextmanager, suppress
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
_TRANSPORT_VECTORS = json.loads(
    (REPOSITORY_ROOT / "test" / "transport" / "testdata" / "loopback_hosts.json").read_text(encoding="utf-8")
)


@contextmanager
def _serve(handler: type[BaseHTTPRequestHandler]) -> Iterator[str]:
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        thread.join(timeout=1)
        server.server_close()


def _prepared(content: str | None = "prepared context", *, status: str = "ready") -> dict[str, object]:
    return {
        "schema": "powercontext.prepared-context.v1",
        "status": status,
        "content": content,
        "content_bytes": 0 if content is None else len(content.encode("utf-8")),
    }


def test_recall_emits_bounded_untrusted_context(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    prepared_content = (
        "PowerContext prepared untrusted historical context.\n\n"
        "BEGIN_POWERCONTEXT_PREPARED_CONTEXT_V1\n"
        '{"trust":"untrusted_history","items":[{"content":"Use the public API."}]}\n'
        "END_POWERCONTEXT_PREPARED_CONTEXT_V1"
    )
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda _query, _scope, *, settings, deadline: _prepared(prepared_content),
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "project:test",
    )
    captured: list[tuple[str, str]] = []
    monkeypatch.setattr(
        recall_module,
        "_capture_prompt",
        lambda _payload, *, prompt, cwd, scope_id, settings, deadline: (
            captured.append((prompt, scope_id)) or {"position": 1}
        ),
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({
                "hook_event_name": "UserPromptSubmit",
                "cwd": "/workspace/project",
                "prompt": "What decisions apply?",
            })
        ),
    )
    output = io.StringIO()
    monkeypatch.setattr(sys, "stdout", output)

    assert recall_module.main() == 0
    context = json.loads(output.getvalue())["hookSpecificOutput"]["additionalContext"]
    assert context == prepared_content
    assert len(context.encode("utf-8")) <= 8_000
    assert captured == [("What decisions apply?", "project:test")]


def test_recall_reads_utf8_stdin_on_windows_encodings(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    prepared_content = "prepared context"
    captured: list[str] = []
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda _query, _scope, *, settings, deadline: _prepared(prepared_content),
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "project:test",
    )
    monkeypatch.setattr(
        recall_module,
        "_capture_prompt",
        lambda _payload, *, prompt, cwd, scope_id, settings, deadline: captured.append(prompt) or {"position": 1},
    )

    payload = {
        "hook_event_name": "UserPromptSubmit",
        "cwd": "/workspace/project",
        "prompt": "查看当前记忆",
    }
    stdin = io.TextIOWrapper(
        io.BytesIO(json.dumps(payload, ensure_ascii=False).encode("utf-8")),
        encoding="cp1252",
    )
    monkeypatch.setattr(sys, "stdin", stdin)
    output = io.StringIO()
    monkeypatch.setattr(sys, "stdout", output)

    assert recall_module.main() == 0
    assert captured == ["查看当前记忆"]
    assert json.loads(output.getvalue())["hookSpecificOutput"]["additionalContext"] == prepared_content


def test_recall_failure_is_non_blocking(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(recall_module._ServerUnavailableError()),
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "project:test",
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({
                "hook_event_name": "UserPromptSubmit",
                "cwd": "/workspace/project",
                "prompt": "Recall context",
            })
        ),
    )
    output = io.StringIO()
    errors = io.StringIO()
    monkeypatch.setattr(sys, "stdout", output)
    monkeypatch.setattr(sys, "stderr", errors)

    assert recall_module.main() == 0
    assert output.getvalue() == ""
    diagnostic = json.loads(errors.getvalue())
    assert diagnostic == {
        "component": "powercontext.codex.recall",
        "event": "context_prepare",
        "outcome": "server_unavailable",
    }


def test_recall_authentication_failure_is_non_blocking_and_content_free(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(recall_module._HttpStatusError(401)),
    )
    errors = io.StringIO()
    monkeypatch.setattr(sys, "stderr", errors)

    assert (
        recall_module._recall_context(
            "secret query",
            "secret scope",
            settings=recall_module.CodexPluginSettings(),
            deadline=time.monotonic() + 1,
        )
        is None
    )
    assert json.loads(errors.getvalue()) == {
        "component": "powercontext.codex.recall",
        "event": "context_prepare",
        "outcome": "authentication_failed",
        "http_status": 401,
    }
    assert "secret" not in errors.getvalue()


def test_recall_records_exact_injected_context_only_when_eval_trace_is_enabled(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    trace = tmp_path / "evaluation-injections.jsonl"
    monkeypatch.setenv("POWERCONTEXT_EVAL_TRACE_PATH", str(trace))
    prepared_context = "PowerContext recalled context: Refresh namespace after writes."
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: _prepared(prepared_context),
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "eval:run-1:on",
    )
    monkeypatch.setattr(recall_module, "_capture_prompt", lambda *_args, **_kwargs: {"position": 1})
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({
                "hook_event_name": "UserPromptSubmit",
                "cwd": "/workspace",
                "prompt": "fix namespace refresh",
                "session_id": "session-1",
                "turn_id": "turn-2",
            })
        ),
    )
    output = io.StringIO()
    monkeypatch.setattr(sys, "stdout", output)

    assert recall_module.main() == 0

    injected = json.loads(output.getvalue())["hookSpecificOutput"]["additionalContext"]
    event = json.loads(trace.read_text())
    assert event == {
        "event_type": "powercontext_injection",
        "observed_at": event["observed_at"],
        "query": "fix namespace refresh",
        "injected_text": injected,
        "hits": [],
        "scope_id": "eval:run-1:on",
        "session_id": "session-1",
        "turn_id": "turn-2",
    }
    assert event["injected_text"] == prepared_context
    assert event["observed_at"].endswith("Z")
    if os.name != "nt":
        assert stat.S_IMODE(trace.stat().st_mode) == 0o600


def test_recall_does_not_write_an_evaluation_trace_by_default(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    monkeypatch.delenv("POWERCONTEXT_EVAL_TRACE_PATH", raising=False)
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: _prepared("PowerContext recalled context: Use memory."),
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "project:test",
    )
    monkeypatch.setattr(recall_module, "_capture_prompt", lambda *_args, **_kwargs: {"position": 1})
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({
                "hook_event_name": "UserPromptSubmit",
                "cwd": "/workspace",
                "prompt": "Recall context",
            })
        ),
    )
    monkeypatch.setattr(sys, "stdout", io.StringIO())

    assert recall_module.main() == 0
    assert list(tmp_path.iterdir()) == []


def test_recall_uses_the_eval_home_when_codex_filters_the_trace_path(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    monkeypatch.delenv("POWERCONTEXT_EVAL_TRACE_PATH", raising=False)
    monkeypatch.setenv("POWERCONTEXT_HOME", str(tmp_path))
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: _prepared("PowerContext recalled context: Use the retained audit."),
    )
    monkeypatch.setattr(recall_module, "_resolve_scope_id", lambda *_args, **_kwargs: "eval:run-1:on")
    monkeypatch.setattr(recall_module, "_capture_prompt", lambda *_args, **_kwargs: {"position": 1})
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": "/workspace", "prompt": "audit injection"})
        ),
    )
    monkeypatch.setattr(sys, "stdout", io.StringIO())

    assert recall_module.main() == 0

    trace = tmp_path / "evaluation-injections.jsonl"
    event = json.loads(trace.read_text())
    assert event["event_type"] == "powercontext_injection"
    assert event["scope_id"] == "eval:run-1:on"
    assert event["injected_text"] == "PowerContext recalled context: Use the retained audit."


@pytest.mark.parametrize("event_name", ["UserPromptSubmit", "user_prompt_submit"])
def test_hook_accepts_codex_event_name_variants(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    event_name: str,
) -> None:
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: _prepared(None, status="empty"),
    )
    captured: list[str] = []
    monkeypatch.setattr(
        recall_module,
        "_capture_prompt",
        lambda _payload, *, prompt, cwd, scope_id, settings, deadline: captured.append(prompt) or {"position": 1},
    )
    monkeypatch.setattr(
        recall_module,
        "_resolve_scope_id",
        lambda _payload, _cwd, *, settings, deadline: "project:test",
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(
            json.dumps({
                "hook_event_name": event_name,
                "cwd": "/workspace/project",
                "prompt": "Capture this input.",
            })
        ),
    )
    monkeypatch.setattr(sys, "stdout", io.StringIO())
    monkeypatch.setattr(sys, "stderr", io.StringIO())

    assert recall_module.main() == 0
    assert captured == ["Capture this input."]


def test_normal_empty_context_emits_a_generic_diagnostic(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: _prepared(None, status="empty"),
    )
    errors = io.StringIO()
    monkeypatch.setattr(sys, "stderr", errors)

    context = recall_module._recall_context(
        "query",
        "project:test",
        settings=recall_module.CodexPluginSettings(),
        deadline=time.monotonic() + 1,
    )

    assert context is None
    diagnostic = json.loads(errors.getvalue())
    assert diagnostic == {
        "component": "powercontext.codex.recall",
        "event": "context_prepare",
        "outcome": "empty",
        "http_status": 200,
        "context_status": "empty",
        "content_bytes": 0,
    }


def test_unknown_prepared_context_schema_fails_open_without_exposing_response(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    response = _prepared("do-not-log")
    response["schema"] = "powercontext.prepared-context.v2"
    monkeypatch.setattr(recall_module, "_prepare_context", lambda *_args, **_kwargs: response)
    errors = io.StringIO()
    monkeypatch.setattr(sys, "stderr", errors)

    context = recall_module._recall_context(
        "secret-query",
        "secret-scope",
        settings=recall_module.CodexPluginSettings(),
        deadline=time.monotonic() + 1,
    )

    assert context is None
    assert json.loads(errors.getvalue())["outcome"] == "invalid_response"
    assert "secret" not in errors.getvalue()


def test_hook_injects_runtime_content_without_a_second_selection(
    recall_module: ModuleType,
) -> None:
    content = "x" * 8_000

    prepared = recall_module._validate_prepared_context(_prepared(content))

    assert prepared["content"] == content


def test_hook_rejects_runtime_content_over_the_requested_budget(recall_module: ModuleType) -> None:
    with pytest.raises(recall_module._InvalidResponseError):
        recall_module._validate_prepared_context(_prepared("x" * 8_001))


def test_context_request_uses_the_prepare_endpoint_once(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    requests: list[tuple[str, dict[str, object], int | None]] = []

    def post(
        path: str,
        payload: dict[str, object],
        *,
        settings: object,
        deadline: float,
        expected_status: int | None = None,
    ) -> dict[str, object]:
        requests.append((path, payload, expected_status))
        return _prepared(None, status="empty")

    monkeypatch.setattr(recall_module, "_post_json", post)

    recall_module._prepare_context(
        "query",
        "project:test",
        settings=recall_module.CodexPluginSettings(),
        deadline=10.0,
    )

    assert requests == [
        (
            "/v1/context/prepare",
            {
                "scope_id": "project:test",
                "query": "query",
                "max_bytes": 8000,
            },
            200,
        )
    ]


def test_hook_resolves_scope_over_mcp_before_prepare_and_capture(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    received: list[tuple[str, str, dict[str, str], dict[str, object] | None]] = []

    class Service(BaseHTTPRequestHandler):
        def _read_json(self) -> dict[str, object]:
            return json.loads(self.rfile.read(int(self.headers["Content-Length"])))

        def _reply_json(self, value: dict[str, object], *, session: bool = False) -> None:
            encoded = json.dumps(value).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            if session:
                self.send_header("Mcp-Session-Id", "resolver-session")
            self.end_headers()
            self.wfile.write(encoded)

        def do_POST(self) -> None:
            payload = self._read_json()
            received.append(("POST", self.path, dict(self.headers), payload))
            if self.path == "/mcp":
                method = payload.get("method")
                if method == "initialize":
                    self._reply_json(
                        {"jsonrpc": "2.0", "id": payload["id"], "result": {"protocolVersion": "2025-11-25"}},
                        session=True,
                    )
                    return
                if method == "notifications/initialized":
                    self.send_response(202)
                    self.end_headers()
                    return
                if method == "tools/call":
                    self._reply_json(
                        {
                            "jsonrpc": "2.0",
                            "id": payload["id"],
                            "result": {
                                "content": [{"type": "text", "text": "resolved"}],
                                "structuredContent": {"scope_id": "scope-bound-to-session"},
                                "isError": False,
                            },
                        }
                    )
                    return
            if self.path == "/v1/context/prepare":
                self._reply_json(_prepared(None, status="empty"))
                return
            if self.path == "/v1/sources/content":
                self._reply_json({"position": 1})
                return
            self.send_error(404)

        def do_DELETE(self) -> None:
            received.append(("DELETE", self.path, dict(self.headers), None))
            self.send_response(202)
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
            pass

    raw_workspace = str(tmp_path)
    monkeypatch.setenv("POWERCONTEXT_CODEX_AUTHORIZATION", "Bearer resolver-token")
    with _serve(Service) as server_url:
        settings = recall_module.CodexPluginSettings()
        object.__setattr__(settings, "server_url", server_url)
        object.__setattr__(settings, "mcp_url", f"{server_url}/mcp")

        def resolve(binding_keys: list[dict[str, str]], **kwargs: object) -> str:
            received.extend(
                [
                    ("POST", "/mcp", {"Authorization": "Bearer resolver-token"}, {"method": "initialize"}),
                    ("POST", "/mcp", {"Authorization": "Bearer resolver-token"}, {"method": "notifications/initialized"}),
                    (
                        "POST",
                        "/mcp",
                        {"Authorization": "Bearer resolver-token"},
                        {"method": "tools/call", "params": {"name": "scope_binding_resolve", "arguments": {"binding_keys": binding_keys}}},
                    ),
                ]
            )
            assert kwargs["explicit_scope_id"] is None
            return "scope-bound-to-session"

        def post(path: str, payload: dict[str, object], **_kwargs: object) -> dict[str, object]:
            received.append(("POST", path, {"Authorization": "Bearer resolver-token"}, payload))
            return _prepared(None, status="empty") if path == "/v1/context/prepare" else {"position": 1}

        monkeypatch.setattr(recall_module._mcp_client, "resolve_scope_binding", resolve)
        monkeypatch.setattr(recall_module, "_post_json", post)
        monkeypatch.setattr(
            sys,
            "stdin",
            io.StringIO(
                json.dumps(
                    {
                        "hook_event_name": "UserPromptSubmit",
                        "cwd": raw_workspace,
                        "prompt": "Remember the bound scope.",
                        "session_id": "session-42",
                    }
                )
            ),
        )
        stdout = io.StringIO()
        stderr = io.StringIO()
        monkeypatch.setattr(sys, "stdout", stdout)
        monkeypatch.setattr(sys, "stderr", stderr)

        assert recall_module.main(settings) == 0

    tool_call = next(payload for method, path, _, payload in received if method == "POST" and path == "/mcp" and payload and payload.get("method") == "tools/call")
    assert tool_call["params"] == {
        "name": "scope_binding_resolve",
        "arguments": {
            "binding_keys": [
                {"integration": "codex", "kind": "session", "external_id": "session-42"},
                {
                    "integration": "codex",
                    "kind": "workspace",
                    "external_id": sha256(raw_workspace.encode("utf-8")).hexdigest(),
                },
            ]
        },
    }
    assert [path for method, path, _, _ in received if method == "POST"] == [
        "/mcp",
        "/mcp",
        "/mcp",
        "/v1/context/prepare",
        "/v1/sources/content",
    ]
    assert all(headers.get("Authorization") == "Bearer resolver-token" for _, _, headers, _ in received)
    downstream = [payload for _, path, _, payload in received if path.startswith("/v1/")]
    assert all(payload is not None and payload["scope_id"] == "scope-bound-to-session" for payload in downstream)
    capture = next(payload for _, path, _, payload in received if path == "/v1/sources/content")
    assert capture is not None and "cwd" not in capture["metadata"]
    assert raw_workspace not in json.dumps([payload for _, _, _, payload in received])
    assert raw_workspace not in stderr.getvalue()
    assert stdout.getvalue() == ""


def test_hook_fails_closed_when_mcp_scope_resolver_returns_a_tool_error(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    received: list[str] = []

    class ToolFailureService(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            received.append(self.path)
            if payload.get("method") == "initialize":
                encoded = json.dumps(
                    {"jsonrpc": "2.0", "id": payload["id"], "result": {"protocolVersion": "2025-11-25"}}
                ).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.send_header("Mcp-Session-Id", "resolver-session")
                self.end_headers()
                self.wfile.write(encoded)
                return
            if payload.get("method") == "notifications/initialized":
                self.send_response(202)
                self.end_headers()
                return
            encoded = json.dumps(
                {"jsonrpc": "2.0", "id": payload["id"], "result": {"content": [], "isError": True}}
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def do_DELETE(self) -> None:
            self.send_response(202)
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
            pass

    raw_workspace = str(tmp_path)
    with _serve(ToolFailureService) as server_url:
        settings = recall_module.CodexPluginSettings()
        object.__setattr__(settings, "server_url", server_url)
        object.__setattr__(settings, "mcp_url", f"{server_url}/mcp")
        monkeypatch.setattr(
            recall_module._mcp_client,
            "resolve_scope_binding",
            lambda *_args, **_kwargs: (received.append("/mcp") or (_ for _ in ()).throw(recall_module._mcp_client.MCPResolutionError())),
        )
        monkeypatch.setattr(
            sys,
            "stdin",
            io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": raw_workspace, "prompt": "Never capture."})),
        )
        stdout = io.StringIO()
        stderr = io.StringIO()
        monkeypatch.setattr(sys, "stdout", stdout)
        monkeypatch.setattr(sys, "stderr", stderr)

        assert recall_module.main(settings) == 0

    assert received == ["/mcp"]
    assert stdout.getvalue() == ""
    assert raw_workspace not in stderr.getvalue()


def test_hook_fails_closed_before_mcp_when_git_root_is_not_utf8(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    called = False
    monkeypatch.setattr(
        recall_module,
        "scope_binding_keys",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid")),
    )
    monkeypatch.setattr(
        recall_module._mcp_client,
        "resolve_scope_binding",
        lambda *_args, **_kwargs: called,
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": "/workspace", "prompt": "Never capture."})),
    )
    monkeypatch.setattr(sys, "stdout", io.StringIO())

    assert recall_module.main() == 0
    assert called is False


def test_hook_forwards_user_explicit_scope_only_to_the_mcp_resolver(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    resolved: list[tuple[list[dict[str, str]], str | None]] = []
    downstream: list[dict[str, object]] = []
    monkeypatch.setenv("POWERCONTEXT_CODEX_SCOPE_ID", "scope-explicit")
    monkeypatch.setattr(
        recall_module._mcp_client,
        "resolve_scope_binding",
        lambda keys, *, explicit_scope_id, **_kwargs: (resolved.append((keys, explicit_scope_id)) or "scope-validated"),
    )
    monkeypatch.setattr(
        recall_module,
        "_post_json",
        lambda _path, payload, **_kwargs: (downstream.append(payload) or _prepared(None, status="empty")),
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": "/workspace", "prompt": "Recall context"})),
    )
    monkeypatch.setattr(sys, "stdout", io.StringIO())

    assert recall_module.main() == 0

    assert resolved[0][1] == "scope-explicit"
    assert downstream[0]["scope_id"] == "scope-validated"


@pytest.mark.parametrize("scope_id", ("", "  "))
def test_hook_preserves_blank_explicit_scope_for_server_validation(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    scope_id: str,
) -> None:
    resolved: list[str | None] = []
    downstream: list[dict[str, object]] = []
    monkeypatch.setenv("POWERCONTEXT_CODEX_SCOPE_ID", scope_id)
    monkeypatch.setattr(
        recall_module._mcp_client,
        "resolve_scope_binding",
        lambda _keys, *, explicit_scope_id, **_kwargs: (
            resolved.append(explicit_scope_id)
            or (_ for _ in ()).throw(recall_module._mcp_client.MCPResolutionError())
        ),
    )
    monkeypatch.setattr(
        recall_module,
        "_post_json",
        lambda _path, payload, **_kwargs: downstream.append(payload),
    )
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": "/workspace", "prompt": "Never recall."})),
    )
    stdout = io.StringIO()
    monkeypatch.setattr(sys, "stdout", stdout)

    assert recall_module.main() == 0

    assert resolved == [scope_id]
    assert downstream == []
    assert stdout.getvalue() == ""


def test_context_prepare_404_is_reported_as_a_version_mismatch(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        recall_module,
        "_prepare_context",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(recall_module._HttpStatusError(404)),
    )
    errors = io.StringIO()
    monkeypatch.setattr(sys, "stderr", errors)

    assert (
        recall_module._recall_context(
            "query",
            "project:test",
            settings=recall_module.CodexPluginSettings(),
            deadline=time.monotonic() + 1,
        )
        is None
    )
    assert json.loads(errors.getvalue())["outcome"] == "version_mismatch"


def test_capture_prompt_is_idempotent_and_preserves_provenance(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    requests: list[tuple[str, dict[str, object]]] = []

    def post(
        path: str,
        payload: dict[str, object],
        *,
        settings: object,
        deadline: float,
    ) -> dict[str, object]:
        requests.append((path, payload))
        return {"position": 1}

    monkeypatch.setattr(recall_module, "_post_json", post)
    hook_payload = {
        "session_id": "session-1",
        "turn_id": "turn-2",
    }

    first = recall_module._capture_prompt(
        hook_payload,
        prompt="Keep the Source pipeline.",
        cwd="/workspace/project",
        scope_id="project:test",
        settings=recall_module.CodexPluginSettings(),
        deadline=10.0,
    )
    second = recall_module._capture_prompt(
        hook_payload,
        prompt="Keep the Source pipeline.",
        cwd="/workspace/project",
        scope_id="project:test",
        settings=recall_module.CodexPluginSettings(),
        deadline=10.0,
    )

    assert first == second == {"position": 1}
    assert requests[0] == requests[1]
    path, payload = requests[0]
    assert path == "/v1/sources/content"
    source_id = payload["source_id"]
    assert isinstance(source_id, str)
    assert source_id.startswith("codex-user-prompt:")
    assert payload["content"] == "Keep the Source pipeline."
    assert payload["metadata"] == {
        "origin": "codex",
        "event": "user_prompt_submit",
        "session_id": "session-1",
        "turn_id": "turn-2",
    }


def test_flush_reaches_the_captured_source_position(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    cursors = iter((1, 3))
    requests: list[tuple[str, dict[str, object]]] = []

    def post(
        path: str,
        payload: dict[str, object],
        *,
        settings: object,
        deadline: float,
    ) -> dict[str, object]:
        requests.append((path, payload))
        return {"current_cursor": next(cursors)}

    monkeypatch.setattr(recall_module, "_post_json", post)

    recall_module._flush_through(
        "project:test",
        3,
        settings=recall_module.CodexPluginSettings(),
        deadline=10.0,
    )

    assert requests == [
        ("/v1/memory/flush", {"scope_id": "project:test"}),
        ("/v1/memory/flush", {"scope_id": "project:test"}),
    ]


def test_flush_is_bounded_by_settings(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls = 0

    def post(*_args: object, **_kwargs: object) -> dict[str, object]:
        nonlocal calls
        calls += 1
        return {"current_cursor": 0}

    monkeypatch.setattr(recall_module, "_post_json", post)

    with pytest.raises(RuntimeError):
        recall_module._flush_through(
            "project:test",
            3,
            settings=recall_module.CodexPluginSettings(flush_max_calls=2),
            deadline=10.0,
        )

    assert calls == 2


def test_hook_refuses_redirects(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    target_headers: list[dict[str, str]] = []

    class TargetHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            target_headers.append(dict(self.headers))
            self.send_response(200)
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
            pass

    with _serve(TargetHandler) as target_url:

        class RedirectHandler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                self.send_response(302)
                self.send_header("Location", f"{target_url}/stolen")
                self.end_headers()

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                pass

        monkeypatch.setenv("POWERCONTEXT_CODEX_AUTHORIZATION", "Bearer secret-token")
        with _serve(RedirectHandler) as source_url:
            settings = recall_module.CodexPluginSettings()
            object.__setattr__(settings, "server_url", source_url)
            with pytest.raises(RuntimeError):
                recall_module._post_json(
                    "/redirect",
                    {"scope_id": "project:test"},
                    settings=settings,
                    deadline=time.monotonic() + 1,
                )

    assert target_headers == []


def test_hook_aborts_a_slow_response_at_the_request_deadline(
    recall_module: ModuleType,
) -> None:
    class SlowHandler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            self.send_response(200)
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"{")
            self.wfile.flush()
            time.sleep(0.2)
            with suppress(BrokenPipeError):
                self.wfile.write(b"}")

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
            pass

    with _serve(SlowHandler) as server_url:
        started = time.monotonic()
        settings = recall_module.CodexPluginSettings(
            request_timeout_seconds=0.05,
            http_budget_seconds=0.1,
        )
        object.__setattr__(settings, "server_url", server_url)
        with pytest.raises(RuntimeError):
            recall_module._post_json(
                "/slow",
                {},
                settings=settings,
                deadline=started + 0.1,
            )

    assert time.monotonic() - started < 0.6


def test_prompt_capture_can_be_disabled(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("POWERCONTEXT_CODEX_CAPTURE_PROMPTS", "false")

    assert recall_module.CodexPluginSettings().capture_prompts is False


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["loopback"])
def test_proxy_handler_bypasses_shared_loopback_hosts(recall_module: ModuleType, host: str) -> None:
    handler = recall_module._LoopbackAwareProxyHandler()
    dead_proxy = "http://127.0.0.1:9"

    loopback = recall_module.Request(f"http://{host}:8000/v1/context/prepare", data=b"{}", method="POST")
    direct_host = loopback.host
    assert handler.proxy_open(loopback, dead_proxy, "http") is None
    assert loopback.host == direct_host


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["non_loopback"])
@pytest.mark.parametrize("scheme", ("http", "https"))
def test_proxy_handler_preserves_remote_proxy_for_shared_non_loopback_hosts(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    host: str,
    scheme: str,
) -> None:
    monkeypatch.setattr("urllib.request.proxy_bypass", lambda _host: False)
    handler = recall_module._LoopbackAwareProxyHandler()
    dead_proxy = "http://127.0.0.1:9"

    remote = recall_module.Request(f"{scheme}://{host}:8000/v1/context/prepare", data=b"{}", method="POST")
    original_host = remote.host
    handler.proxy_open(remote, dead_proxy, scheme)

    assert remote.host == "127.0.0.1:9"
    if scheme == "https":
        assert remote._tunnel_host == original_host
