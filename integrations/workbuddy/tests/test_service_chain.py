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

import importlib.util
import io
import json
import os
import subprocess
import sys
import textwrap
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from hashlib import sha256
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import ModuleType
from urllib.request import Request

import pytest


REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"
_TRANSPORT_VECTORS = json.loads(
    (REPOSITORY_ROOT / "test" / "transport" / "testdata" / "loopback_hosts.json").read_text(encoding="utf-8")
)
_DEAD_PROXY = "http://127.0.0.1:9"
_PROXY_ENVIRONMENT_NAMES = frozenset({"http_proxy", "https_proxy", "ftp_proxy", "all_proxy", "no_proxy"})

# ``_URL_OPENER`` is built once at import time and ``ProxyHandler`` reads the
# proxy configuration while it is constructed, so the proxy environment has to
# exist before the hook module is imported. Reloading the module inside the
# current process cannot undo the handler that was already installed, therefore
# every proxy regression test runs the hook entry point in a fresh interpreter.
_PROXY_CHILD = textwrap.dedent(
    '''
    import importlib.util
    import io
    import json
    import sys

    tests_path, server_url, cwd = sys.argv[1:4]

    def _load(path, name):
        spec = importlib.util.spec_from_file_location(name, path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
        return module

    tests = _load(tests_path, "powercontext_workbuddy_proxy_child")
    hook = tests.load_hook_module()

    settings = hook.WorkBuddyPluginSettings(
        server_url=server_url,
        scope_mode="project",
        authorization_environment="WORKBUDDY_TOKEN",
        authorization="Bearer workbuddy-runtime-token",
        request_timeout_seconds=1.0,
        request_budget_seconds=2.0,
        prepare_max_bytes=64,
        source_max_bytes=128,
    )
    payload = {"hook_event_name": "UserPromptSubmit", "cwd": cwd, "prompt": "Which policy applies?", "session_id": "session-1"}
    real_stdout = sys.stdout
    captured = io.StringIO()
    sys.stdin = io.StringIO(json.dumps(payload))
    sys.stdout = captured
    exit_code = hook.main(settings)
    sys.stdout = real_stdout
    print("HOOK-EXIT", exit_code)
    print("HOOK-STDOUT", captured.getvalue().strip())
    '''
)

# The control probe proves that the injected proxy environment really intercepts
# loopback traffic, so a passing hook run cannot be explained by a proxy that
# never took effect.
_CONTROL_CHILD = textwrap.dedent(
    '''
    import sys
    from urllib.request import Request, build_opener

    server_url = sys.argv[1]
    request = Request(
        server_url + "/v1/context/prepare",
        data=b"{}",
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with build_opener().open(request, timeout=5) as response:
            print("CONTROL-RESULT", "HTTP", response.status)
    except Exception as error:
        print("CONTROL-RESULT", type(error).__name__)
    '''
)


def load_hook_module() -> ModuleType:
    hooks = PLUGIN_ROOT / "hooks"
    scripts = PLUGIN_ROOT / "scripts"
    sys.path[:0] = [str(hooks), str(scripts)]
    path = hooks / "workbuddy_powercontext_hook.py"
    spec = importlib.util.spec_from_file_location("powercontext_workbuddy_service_chain", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


@contextmanager
def serve(handler: type[BaseHTTPRequestHandler]) -> Iterator[str]:
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()


def test_hook_uses_the_live_service_chain_with_bounded_requests(monkeypatch, tmp_path: Path) -> None:
    received: list[tuple[str, dict[str, str], dict[str, object]]] = []

    class Service(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API.
            content_length = int(self.headers["Content-Length"])
            request = json.loads(self.rfile.read(content_length))
            received.append((self.path, dict(self.headers), request))
            if self.path == "/v1/context/prepare":
                self._respond(200, {"schema": "powercontext.prepared-context.v1", "status": "ready", "content": "Use approved policy.", "content_bytes": 20})
                return
            if self.path == "/v1/sources/content":
                self._respond(201, {"position": 3})
                return
            self._respond(404, {})

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

        def _respond(self, status: int, payload: dict[str, object]) -> None:
            encoded = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

    hook = load_hook_module()
    authorization = "Bearer workbuddy-runtime-token"
    with serve(Service) as server_url:
        settings = hook.WorkBuddyPluginSettings(
            server_url=server_url,
            scope_mode="project",
            authorization_environment="WORKBUDDY_TOKEN",
            authorization=authorization,
            request_timeout_seconds=1.0,
            request_budget_seconds=2.0,
            prepare_max_bytes=64,
            source_max_bytes=128,
        )
        prompt = "Which policy applies?"
        cwd = str(tmp_path)
        scope_id = "local:" + sha256(os.fsencode(tmp_path.resolve())).hexdigest()
        payload = {"hook_event_name": "UserPromptSubmit", "cwd": cwd, "prompt": prompt, "session_id": "session-1"}
        monkeypatch.setattr(sys, "stdin", io.StringIO(json.dumps(payload)))
        stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", stdout)

        assert hook.main(settings) == 0

    assert json.loads(stdout.getvalue()) == {
        "hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": "Use approved policy."}
    }
    assert authorization not in stdout.getvalue()
    assert received[0][0] == "/v1/context/prepare"
    assert received[0][1]["Authorization"] == authorization
    assert received[0][2] == {"scope_id": scope_id, "query": prompt, "max_bytes": 64}
    assert received[1][0] == "/v1/sources/content"
    assert received[1][2]["source_id"] == "workbuddy-user-prompt:" + sha256(
        f"{scope_id}\0session-1\0\0{prompt}".encode("utf-8")
    ).hexdigest()


def test_hook_keeps_context_empty_and_captures_the_prompt_when_prepare_is_empty(monkeypatch, tmp_path: Path) -> None:
    received: list[tuple[str, dict[str, object]]] = []

    class EmptyContextService(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API.
            content_length = int(self.headers["Content-Length"])
            received.append((self.path, json.loads(self.rfile.read(content_length))))
            if self.path == "/v1/context/prepare":
                self._respond(200, {"schema": "powercontext.prepared-context.v1", "status": "empty", "content": None, "content_bytes": 0})
                return
            if self.path == "/v1/sources/content":
                self._respond(201, {"position": 3})
                return
            self._respond(404, {})

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

        def _respond(self, status: int, payload: dict[str, object]) -> None:
            encoded = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

    hook = load_hook_module()
    prompt = "What context is available?"
    with serve(EmptyContextService) as server_url:
        settings = hook.WorkBuddyPluginSettings(
            server_url=server_url,
            scope_mode="project",
            authorization_environment="WORKBUDDY_TOKEN",
            authorization="Bearer test-token",
            request_timeout_seconds=1.0,
            request_budget_seconds=2.0,
            prepare_max_bytes=64,
            source_max_bytes=128,
        )
        monkeypatch.setattr(
            sys,
            "stdin",
            io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": str(tmp_path), "prompt": prompt})),
        )
        stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", stdout)

        assert hook.main(settings) == 0

    assert json.loads(stdout.getvalue()) == {
        "hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": ""}
    }
    assert [path for path, _ in received] == ["/v1/context/prepare", "/v1/sources/content"]
    assert received[1][1]["content"] == prompt


def test_hook_preserves_the_host_prompt_when_the_live_service_is_unavailable(monkeypatch, tmp_path: Path) -> None:
    class UnavailableService(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API.
            self.send_response(503)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

    hook = load_hook_module()
    prompt_secret = "workbuddy-prompt-secret"
    authorization_secret = "workbuddy-runtime-secret"
    with serve(UnavailableService) as server_url:
        settings = hook.WorkBuddyPluginSettings(
            server_url=server_url,
            scope_mode="project",
            authorization_environment="WORKBUDDY_TOKEN",
            authorization=f"Bearer {authorization_secret}",
            request_timeout_seconds=1.0,
            request_budget_seconds=2.0,
            prepare_max_bytes=64,
            source_max_bytes=128,
        )
        monkeypatch.setattr(
            sys,
            "stdin",
            io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": str(tmp_path), "prompt": prompt_secret})),
        )
        stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", stdout)

        assert hook.main(settings) == 0

    assert json.loads(stdout.getvalue()) == {
        "hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": ""}
    }
    assert prompt_secret not in stdout.getvalue()
    assert authorization_secret not in stdout.getvalue()


def test_hook_rejects_invalid_persisted_configuration_instead_of_falling_back_to_environment(
    monkeypatch,
    tmp_path: Path,
) -> None:
    hook = load_hook_module()
    (tmp_path / "powercontext.json").write_text(
        json.dumps(
            {
                "schema": 1,
                "server_url": "http://127.0.0.1:8000",
                "authorization": "Bearer persisted-secret",
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(hook, "_PLUGIN_ROOT", tmp_path)
    monkeypatch.setenv("POWERCONTEXT_WORKBUDDY_SERVER_URL", "http://127.0.0.1:8000")
    monkeypatch.setattr(
        sys,
        "stdin",
        io.StringIO(json.dumps({"hook_event_name": "UserPromptSubmit", "cwd": str(tmp_path), "prompt": "hello"})),
    )
    stdout = io.StringIO()
    monkeypatch.setattr(sys, "stdout", stdout)

    assert hook.main() == 0
    assert stdout.getvalue() == ""


def _run_proxy_child(script: str, *arguments: str) -> subprocess.CompletedProcess[str]:
    """Run a child interpreter under an isolated dead-proxy environment."""
    environment = dict(os.environ)
    for name in tuple(environment):
        if name.lower() in _PROXY_ENVIRONMENT_NAMES:
            environment.pop(name)
    environment["http_proxy"] = _DEAD_PROXY
    environment["https_proxy"] = _DEAD_PROXY
    return subprocess.run(
        [sys.executable, "-c", script, *arguments],
        capture_output=True,
        text=True,
        env=environment,
        cwd=PLUGIN_ROOT.parent.parent,
        timeout=30,
    )


def test_proxy_environment_does_not_redirect_loopback_hook_traffic(tmp_path: Path) -> None:
    """A configured proxy must never receive loopback PowerContext requests or the bearer credential."""
    received: list[tuple[str, dict[str, str], dict[str, object]]] = []

    class ProxyAwareService(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API.
            content_length = int(self.headers["Content-Length"])
            request = json.loads(self.rfile.read(content_length))
            received.append((self.path, dict(self.headers), request))
            if self.path == "/v1/context/prepare":
                self._respond(200, {"schema": "powercontext.prepared-context.v1", "status": "ready", "content": "Use approved policy.", "content_bytes": 20})
                return
            if self.path == "/v1/sources/content":
                self._respond(201, {"position": 3})
                return
            self._respond(404, {})

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

        def _respond(self, status: int, payload: dict[str, object]) -> None:
            encoded = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

    with serve(ProxyAwareService) as server_url:
        result = _run_proxy_child(_PROXY_CHILD, __file__, server_url, str(tmp_path))

    assert result.returncode == 0, result.stderr
    assert "HOOK-EXIT 0" in result.stdout, result.stdout
    assert "Use approved policy." in result.stdout, result.stdout
    assert "workbuddy-runtime-token" not in result.stdout, "hook output must not leak the bearer credential"
    assert received, "the hook never reached the local service"
    assert received[0][0] == "/v1/context/prepare"
    assert received[0][1]["Authorization"] == "Bearer workbuddy-runtime-token"
    assert received[1][0] == "/v1/sources/content"


def test_control_probe_intercepts_loopback_when_parent_bypasses_all_hosts(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The default urllib opener must fail through the dead proxy, proving the proxy really took effect."""
    monkeypatch.setenv("NO_PROXY", "*")

    class NoopService(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API.
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

    with serve(NoopService) as server_url:
        result = _run_proxy_child(_CONTROL_CHILD, server_url)

    assert result.returncode == 0, result.stderr
    assert "CONTROL-RESULT URLError" in result.stdout, result.stdout


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["loopback"])
def test_loopback_aware_proxy_handler_bypasses_shared_loopback_hosts(
    monkeypatch: pytest.MonkeyPatch,
    host: str,
) -> None:
    """Every shared loopback authority must connect directly without request rewriting."""
    monkeypatch.setattr("urllib.request.proxy_bypass", lambda host: False)
    hook = load_hook_module()
    handler = hook._LoopbackAwareProxyHandler()

    loopback = Request(f"http://{host}:8000/v1/context/prepare", data=b"{}", method="POST")
    direct_host = loopback.host
    assert handler.proxy_open(loopback, _DEAD_PROXY, "http") is None
    assert loopback.host == direct_host, "loopback request must keep its direct destination"


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["non_loopback"])
@pytest.mark.parametrize("scheme", ("http", "https"))
def test_loopback_aware_proxy_handler_preserves_remote_proxy_behaviour(
    monkeypatch: pytest.MonkeyPatch,
    host: str,
    scheme: str,
) -> None:
    """Every shared non-loopback authority must retain configured proxy routing."""
    monkeypatch.setattr("urllib.request.proxy_bypass", lambda host: False)
    hook = load_hook_module()
    handler = hook._LoopbackAwareProxyHandler()

    remote = Request(f"{scheme}://{host}:8000/v1/context/prepare", data=b"{}", method="POST")
    original_host = remote.host
    handler.proxy_open(remote, _DEAD_PROXY, scheme)

    assert remote.host == "127.0.0.1:9", "remote request must be routed through the proxy"
    if scheme == "https":
        assert remote._tunnel_host == original_host, "remote HTTPS request must tunnel to its original destination"
