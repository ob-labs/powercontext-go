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
import json
import os
import subprocess
import sys
import threading
import time
from collections.abc import Iterator
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
PLUGIN_ROOT = REPOSITORY_ROOT / "integrations" / "codex" / "plugins" / "powercontext"

_HOOK_CHILD = """
import sys
from pathlib import Path

plugin_root = Path(sys.argv[1])
configuration_path = Path(sys.argv[2])
sys.path.insert(0, str(plugin_root))

import settings

settings._MCP_CONFIGURATION_PATH = configuration_path
from hooks import recall

recall._resolve_scope_id = lambda *_args, **_kwargs: "scope-bound-by-test"
raise SystemExit(recall.main())
"""

_LOCK_HOLDER = """
import os
import sys
from pathlib import Path

path = Path(sys.argv[1])
path.parent.mkdir(parents=True, exist_ok=True)
with path.open("a+b") as lock_file:
    if os.name == "nt":
        import msvcrt

        lock_file.seek(0, os.SEEK_END)
        if lock_file.tell() == 0:
            lock_file.write(b"\\0")
            lock_file.flush()
        lock_file.seek(0)
        msvcrt.locking(lock_file.fileno(), msvcrt.LK_LOCK, 1)
    else:
        import fcntl

        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX)
    print("ready", flush=True)
    sys.stdin.read(1)
"""


@contextmanager
def _serve_unavailable() -> Iterator[str]:
    class Service(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            self.send_response(503)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, _format: str, *_arguments: object) -> None:
            return

    server = ThreadingHTTPServer(("127.0.0.1", 0), Service)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()


def _run_hook(
    configuration_path: Path,
    payload: dict[str, object],
    state_path: Path,
) -> subprocess.CompletedProcess[str]:
    environment = dict(os.environ)
    environment["POWERCONTEXT_DIAGNOSTIC_STATE_FILE"] = str(state_path)
    environment["POWERCONTEXT_CODEX_AUTHORIZATION"] = "Bearer diagnostic-test-token"
    return subprocess.run(
        [sys.executable, "-c", _HOOK_CHILD, str(PLUGIN_ROOT), str(configuration_path)],
        input=json.dumps(payload),
        capture_output=True,
        text=True,
        env=environment,
        timeout=15,
        check=False,
    )


def _load_diagnostics() -> object:
    path = PLUGIN_ROOT / "hooks" / "diagnostics.py"
    spec = importlib.util.spec_from_file_location(
        "powercontext_codex_diagnostics", path
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_unavailable_hook_diagnostic_is_rate_limited_across_processes(
    tmp_path: Path,
) -> None:
    state_path = tmp_path / "codex-diagnostics.json"
    state_path.write_text("not json", encoding="utf-8")
    private_workspace = tmp_path / "private workspace"
    prompt = "query with diagnostic-test-token and private content"
    payload = {
        "hook_event_name": "UserPromptSubmit",
        "cwd": str(private_workspace),
        "prompt": prompt,
    }

    with _serve_unavailable() as server_url:
        configuration_path = tmp_path / "mcp.json"
        configuration_path.write_text(
            json.dumps(
                {
                    "mcpServers": {
                        "powercontext": {
                            "type": "http",
                            "url": f"{server_url}/mcp",
                            "required": False,
                            "env_http_headers": {
                                "Authorization": "POWERCONTEXT_CODEX_AUTHORIZATION"
                            },
                        }
                    }
                }
            ),
            encoding="utf-8",
        )
        first = _run_hook(configuration_path, payload, state_path)
        second = _run_hook(configuration_path, payload, state_path)

    assert first.returncode == 0, first.stderr
    assert first.stdout == ""
    assert json.loads(first.stderr) == {
        "component": "powercontext.codex.recall",
        "event": "context_prepare",
        "outcome": "server_unavailable",
        "http_status": 503,
    }
    assert second.returncode == 0, second.stderr
    assert second.stdout == ""
    assert second.stderr == ""
    assert json.loads(state_path.read_text(encoding="utf-8")).keys() == {
        "server_unavailable"
    }
    assert state_path.with_name(f"{state_path.name}.lock").is_file()
    assert list(tmp_path.glob(f".{state_path.name}.*.tmp")) == []
    for protected in (
        server_url,
        "diagnostic-test-token",
        prompt,
        str(private_workspace),
        "scope-bound-by-test",
    ):
        assert protected not in first.stderr


def test_diagnostic_lock_contention_returns_without_waiting(
    monkeypatch, tmp_path: Path
) -> None:
    diagnostics = _load_diagnostics()
    state_path = tmp_path / "codex-diagnostics.json"
    lock_path = state_path.with_name(f"{state_path.name}.lock")
    monkeypatch.setenv("POWERCONTEXT_DIAGNOSTIC_STATE_FILE", str(state_path))
    holder = subprocess.Popen(
        [sys.executable, "-c", _LOCK_HOLDER, str(lock_path)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    try:
        assert holder.stdout is not None
        assert holder.stdout.readline().strip() == "ready"
        started = time.monotonic()
        assert diagnostics.should_emit("server_unavailable") is True
        assert time.monotonic() - started < 0.6
        assert not state_path.exists()
    finally:
        if holder.stdin is not None:
            holder.stdin.write("\n")
            holder.stdin.flush()
        try:
            holder.wait(timeout=2)
        except subprocess.TimeoutExpired:
            holder.kill()
            holder.wait(timeout=2)
