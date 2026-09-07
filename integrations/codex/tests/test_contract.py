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
import json
from pathlib import Path
from types import ModuleType
from typing import Any
from urllib.request import Request

import pytest
from pydantic import ValidationError

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
PLUGIN_ROOT = REPOSITORY_ROOT / "integrations" / "codex" / "plugins" / "powercontext"
_TRANSPORT_VECTORS = json.loads(
    (REPOSITORY_ROOT / "test" / "transport" / "testdata" / "loopback_hosts.json").read_text(encoding="utf-8")
)


def test_scope_binding_keys_prioritize_session_and_hash_the_unmodified_git_root(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    git_root = "C:\\工程\\private repository "
    monkeypatch.setattr(
        scope_module,
        "_git_value",
        lambda _cwd, *arguments: git_root if arguments == ("rev-parse", "--show-toplevel") else None,
    )

    keys = scope_module.scope_binding_keys("C:\\fallback", session_id="session-42")

    assert keys == [
        {"integration": "codex", "kind": "session", "external_id": "session-42"},
        {
            "integration": "codex",
            "kind": "workspace",
            "external_id": sha256(git_root.encode("utf-8")).hexdigest(),
        },
    ]
    assert all(git_root not in str(key) and "C:\\fallback" not in str(key) for key in keys)


@pytest.mark.parametrize(
    "remote",
    (
        "https://github.com/OceanBase/powercontext.git",
        "ssh://git@github.com/OceanBase/powercontext.git",
        "git@github.com:OceanBase/powercontext.git",
    ),
)
def test_scope_normalizes_network_git_remotes(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    remote: str,
) -> None:
    """Server-owned workspace bindings must never derive a Scope from a remote."""

    calls: list[tuple[str, ...]] = []

    def git_value(_cwd: str, *arguments: str) -> str | None:
        calls.append(arguments)
        if arguments == ("rev-parse", "--show-toplevel"):
            return "C:\\workspace"
        return remote

    monkeypatch.setattr(scope_module, "_git_value", git_value)

    keys = scope_module.scope_binding_keys("C:\\fallback")

    assert calls == [("rev-parse", "--show-toplevel")]
    assert keys == [
        {
            "integration": "codex",
            "kind": "workspace",
            "external_id": sha256(b"C:\\workspace").hexdigest(),
        }
    ]


def test_scope_override_wins(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An explicit host override remains distinct from an omitted override."""

    monkeypatch.setenv("POWERCONTEXT_CODEX_SCOPE_ID", "scope-explicit")

    assert scope_module.CodexPluginSettings().scope_id == "scope-explicit"


def test_scope_binding_routes_through_server(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """The legacy private-binding case now persists only through the Server client."""

    workspace = str(tmp_path / "workspace")
    calls: list[tuple[str, object]] = []
    monkeypatch.setattr(scope_module, "_git_value", lambda *_args: workspace)
    monkeypatch.setattr(
        scope_module.mcp_client,
        "set_workspace_scope_binding",
        lambda key, scope_id, **_kwargs: calls.append(("set", (key, scope_id))),
    )
    monkeypatch.setattr(
        scope_module.mcp_client,
        "resolve_scope_binding",
        lambda keys, **_kwargs: calls.append(("resolve", keys)) or "scope-server",
    )

    assert scope_module.main(["--cwd", workspace, "--bind-workstream", "scope-server"]) == 0
    assert calls[0][0] == "set"
    assert calls[1][0] == "resolve"
    assert not (tmp_path / ".git" / "powercontext" / "codex-workspace.json").exists()


def test_scope_binding_accepts_non_git_workspace(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A non-Git cwd remains an opaque hashed Server binding identity."""

    cwd = "C:\\not-a-git-workspace"
    monkeypatch.setattr(scope_module, "_git_value", lambda *_args: None)

    assert scope_module.scope_binding_keys(cwd) == [
        {
            "integration": "codex",
            "kind": "workspace",
            "external_id": sha256(cwd.encode("utf-8")).hexdigest(),
        }
    ]


def test_mcp_scope_resolver_uses_session_headers_and_the_authorization_reference(
    mcp_client_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class Response:
        def __init__(self, status: int, body: dict[str, object], headers: dict[str, str] | None = None) -> None:
            self.status = status
            self.headers = {"Content-Type": "application/json", **(headers or {})}
            self._body = json.dumps(body).encode("utf-8")

        def __enter__(self) -> Response:
            return self

        def __exit__(self, *args: object) -> None:
            return None

        def read(self, amount: int = -1) -> bytes:
            chunk, self._body = self._body[:amount], self._body[amount:]
            return chunk

    class Opener:
        def __init__(self) -> None:
            self.requests: list[Request] = []
            self.responses = iter(
                [
                    Response(200, {"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": "2025-11-25"}}, {"mcp-session-id": "session"}),
                    Response(202, {}),
                    Response(200, {"jsonrpc": "2.0", "id": 2, "result": {"content": [], "isError": False, "structuredContent": {"scope_id": "scope-1"}}}),
                    Response(202, {}),
                ]
            )

        def open(self, request: Request, timeout: float) -> Response:
            self.requests.append(request)
            return next(self.responses)

    monkeypatch.setenv("POWERCONTEXT_CODEX_AUTHORIZATION", "Bearer resolver-token")
    settings = mcp_client_module.CodexPluginSettings()
    opener = Opener()
    monkeypatch.setattr(mcp_client_module, "_URL_OPENER", opener)

    assert mcp_client_module.resolve_scope_binding(
        [{"integration": "codex", "kind": "workspace", "external_id": "digest"}],
        explicit_scope_id="scope-explicit",
        settings=settings,
        deadline=mcp_client_module.monotonic() + 1,
    ) == "scope-1"

    bodies = [json.loads(request.data) for request in opener.requests[:3]]
    assert bodies[2]["params"] == {
        "name": "scope_binding_resolve",
        "arguments": {
            "explicit_scope_id": "scope-explicit",
            "binding_keys": [{"integration": "codex", "kind": "workspace", "external_id": "digest"}],
        },
    }
    assert all(request.get_header("Authorization") == "Bearer resolver-token" for request in opener.requests)
    assert all(request.full_url == "http://127.0.0.1:8000/mcp/" for request in opener.requests)
    assert opener.requests[1].get_header("Mcp-session-id") == "session"
    assert opener.requests[2].get_header("Mcp-protocol-version") == "2025-11-25"
    assert opener.requests[2].get_header("Mcp-session-id") == "session"
    assert opener.requests[3].get_header("Mcp-session-id") == "session"
    assert opener.requests[3].get_method() == "DELETE"


def test_mcp_proxy_handler_bypasses_loopback_destinations(mcp_client_module: ModuleType) -> None:
    request = mcp_client_module.Request("http://127.0.0.1:8000/mcp", data=b"{}", method="POST")

    assert mcp_client_module._LoopbackAwareProxyHandler().proxy_open(request, "http://127.0.0.1:9", "http") is None


def test_codex_settings_precedence_and_validation(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("POWERCONTEXT_CODEX_SERVER_URL", "https://environment.example/api/")
    monkeypatch.setenv("POWERCONTEXT_CODEX_CAPTURE_PROMPTS", "false")
    monkeypatch.setenv("POWERCONTEXT_CODEX_REQUEST_TIMEOUT_SECONDS", "4.5")

    environment = recall_module.CodexPluginSettings()
    explicit = recall_module.CodexPluginSettings(server_url="https://explicit.example/")

    assert environment.server_url == "http://127.0.0.1:8000"
    assert environment.mcp_url == "http://127.0.0.1:8000/mcp/"
    assert environment.capture_prompts is False
    assert environment.request_timeout_seconds == 4.5
    assert explicit.server_url == "http://127.0.0.1:8000"


def test_codex_settings_load_the_optional_mcp_authorization_environment(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("POWERCONTEXT_CODEX_AUTHORIZATION", "Bearer secret-token")

    settings = recall_module.CodexPluginSettings()

    assert settings.authorization is not None
    assert settings.authorization.get_secret_value() == "Bearer secret-token"
    assert "secret-token" not in repr(settings)


def test_codex_mcp_uses_an_optional_authorization_environment() -> None:
    plugin_root = Path(__file__).resolve().parents[3] / "integrations" / "codex" / "plugins" / "powercontext"
    configuration = json.loads((plugin_root / ".mcp.json").read_text())

    assert configuration["mcpServers"]["powercontext"]["env_http_headers"] == {
        "Authorization": "POWERCONTEXT_CODEX_AUTHORIZATION"
    }
    assert "http_headers" not in configuration["mcpServers"]["powercontext"]


@pytest.mark.parametrize(
    "authorization",
    ["Basic secret-token", "Bearer ", "Bearer token with spaces", "Bearer token\nsecond-header"],
)
def test_codex_settings_reject_invalid_authorization_headers(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    authorization: str,
) -> None:
    monkeypatch.setenv("POWERCONTEXT_CODEX_AUTHORIZATION", authorization)

    with pytest.raises(ValidationError):
        recall_module.CodexPluginSettings()


def test_codex_settings_ignore_unscoped_legacy_names(
    recall_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("POWERCONTEXT_HTTP_URL", "https://legacy.example")

    assert recall_module.CodexPluginSettings().server_url == "http://127.0.0.1:8000"


@pytest.mark.parametrize(
    "value",
    [
        "http://memory.example.com/mcp",
        "https://user:password@memory.example.com/mcp",
        "https://memory.example.com/mcp?token=secret",
        "https://memory.example.com/mcp#fragment",
        "https://memory.example.com/api",
        "file:///tmp/socket/mcp",
    ],
)
def test_codex_settings_reject_unsafe_or_ambiguous_mcp_urls(
    settings_module: ModuleType,
    value: str,
) -> None:
    with pytest.raises(ValueError):
        settings_module._http_base_url(value)


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["loopback"])
def test_codex_settings_accept_shared_loopback_hosts(settings_module: ModuleType, host: str) -> None:
    settings_module._http_base_url(f"http://{host}:8000/mcp")


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["non_loopback"])
def test_codex_settings_reject_shared_non_loopback_plaintext_hosts(settings_module: ModuleType, host: str) -> None:
    with pytest.raises(ValueError):
        settings_module._http_base_url(f"http://{host}:8000/mcp")


def test_codex_settings_normalize_the_mcp_path_to_http_base(
    settings_module: ModuleType,
) -> None:
    assert settings_module._http_base_url("https://memory.example/api/mcp/") == "https://memory.example/api"


def test_git_scope_value_decodes_only_utf8_bytes_and_preserves_trailing_space(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    raw = "C:\\工程\\private repository ".encode("utf-8") + b"\r\n"
    monkeypatch.setattr(
        scope_module.subprocess,
        "run",
        lambda *_args, **_kwargs: type("Completed", (), {"stdout": raw})(),
    )

    assert scope_module._git_value("C:\\fallback", "rev-parse", "--show-toplevel") == "C:\\工程\\private repository "


@pytest.mark.parametrize("terminator", (b"\n", b"\r\n"))
def test_git_scope_value_removes_only_the_terminal_line_ending(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    terminator: bytes,
) -> None:
    raw = b"C:\\workspace " + terminator
    monkeypatch.setattr(
        scope_module.subprocess,
        "run",
        lambda *_args, **_kwargs: type("Completed", (), {"stdout": raw})(),
    )

    assert scope_module._git_value("C:\\fallback", "rev-parse", "--show-toplevel") == "C:\\workspace "


def test_invalid_git_utf8_does_not_fall_back_to_a_local_scope(
    scope_module: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        scope_module.subprocess,
        "run",
        lambda *_args, **_kwargs: type("Completed", (), {"stdout": b"\xff\r\n"})(),
    )

    with pytest.raises(UnicodeDecodeError):
        scope_module.scope_binding_keys("C:\\fallback")


def test_project_context_skill_uses_the_high_level_work_continuity_loop() -> None:
    content = (PLUGIN_ROOT / "skills" / "project-context" / "SKILL.md").read_text(encoding="utf-8")

    assert 'description: Create and commit a current-work Handoff when the user says "交接"' in content
    assert '"$PLUGIN_ROOT/.venv/bin/python" "$PLUGIN_ROOT/scripts/project_scope.py"' in content
    assert "uv run --frozen" not in content
    assert "create_work_contract" in content
    assert "select_handoff_workstream" in content
    assert "handoff_current_work" in content
    assert "acknowledge_handoff" in content
    assert "record_task_outcome" in content
    assert "Complete a one-turn durable Handoff" in content
    assert "do not ask for a second confirmation" in content
    assert "native\npicker" in content
    assert "Pass the returned `handoff` member unchanged" in content
    assert "no durable Handoff milestone was committed" in content
    assert "canonical temporary carrier" in content
    assert 'selection: "prepared"' in content
    assert "call `commit_handoff` only when" in content
    assert "Do not treat every session stop as task completion" in content


def test_powercontext_plugin_advertises_the_one_turn_handoff() -> None:
    manifest = json.loads((PLUGIN_ROOT / ".codex-plugin" / "plugin.json").read_text())
    prompts = manifest["interface"]["defaultPrompt"]

    assert len(prompts) <= 3
    assert all(len(prompt) <= 128 for prompt in prompts)
    assert "Hand off and commit the current work in one turn." in prompts
