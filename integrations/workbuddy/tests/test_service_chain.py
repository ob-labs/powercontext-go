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
import sys
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from hashlib import sha256
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import ModuleType


PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"


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
