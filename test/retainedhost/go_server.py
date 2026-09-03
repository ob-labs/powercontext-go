# Copyright (c) 2026 OceanBase.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Run the real Go HTTP stack for retained-adapter tests."""

from __future__ import annotations

import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
from collections import deque
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from shutil import which
from urllib.request import ProxyHandler, build_opener

_ROOT = Path(__file__).resolve().parents[2]
_BUILD_DIR = Path(tempfile.gettempdir()) / "powercontext-retainedhost-server"
_PROXY_NAMES = ("ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "all_proxy", "http_proxy", "https_proxy")


@dataclass(frozen=True, slots=True)
class GoServer:
    """One loopback-only PowerContext Go Server process."""

    base_url: str


def _binary_path() -> Path:
    suffix = ".exe" if os.name == "nt" else ""
    return _BUILD_DIR / f"retainedhost-server{suffix}"


def _build_server() -> Path:
    binary = _binary_path()
    if binary.is_file():
        return binary
    go = which("go")
    if go is None:
        raise RuntimeError("Go is required to run retained host adapter service-chain tests")
    _BUILD_DIR.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        [go, "build", "-tags", "sqlite_fts5", "-o", str(binary), "./test/retainedhost/server"],
        cwd=_ROOT,
        capture_output=True,
        text=True,
        timeout=180,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"build retained host test server failed:\n{result.stdout}\n{result.stderr}")
    return binary


def _free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


@contextmanager
def _without_proxy_environment() -> Iterator[None]:
    previous = {name: os.environ.get(name) for name in (*_PROXY_NAMES, "NO_PROXY", "no_proxy")}
    for name in _PROXY_NAMES:
        os.environ.pop(name, None)
    os.environ["NO_PROXY"] = "127.0.0.1,localhost,::1"
    os.environ["no_proxy"] = os.environ["NO_PROXY"]
    try:
        yield
    finally:
        for name, value in previous.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value


@contextmanager
def running_go_server(tmp_path: Path, *, source_window_limit: int = 100) -> Iterator[GoServer]:
    """Start a fresh SQLite-backed Server and prove its public health endpoint."""

    if sys.platform == "win32":
        import pytest

        pytest.skip("retained Go Server process tests run on the supported Linux CI host")
    binary = _build_server()
    port = _free_port()
    output: deque[str] = deque(maxlen=80)
    with _without_proxy_environment():
        process = subprocess.Popen(
            [
                str(binary),
                "--host",
                "127.0.0.1",
                "--port",
                str(port),
                "--database",
                str(tmp_path / "powercontext.db"),
                "--source-window-limit",
                str(source_window_limit),
            ],
            cwd=_ROOT,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        assert process.stdout is not None
        reader = threading.Thread(target=lambda: output.extend(process.stdout), daemon=True)
        reader.start()
        server = GoServer(base_url=f"http://127.0.0.1:{port}")
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    break
                try:
                    response = build_opener(ProxyHandler({})).open(f"{server.base_url}/health/ready", timeout=1)
                    if response.status == 200:
                        break
                except OSError:
                    time.sleep(0.05)
            else:
                raise RuntimeError("retained Go Server did not become ready")
            if process.poll() is not None:
                raise RuntimeError(f"retained Go Server exited with {process.returncode}: {''.join(output)}")
            yield server
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
