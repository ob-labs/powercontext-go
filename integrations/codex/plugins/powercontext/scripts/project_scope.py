#!/usr/bin/env python3
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

"""Resolve Codex workspace bindings without deriving local Scope IDs."""

from __future__ import annotations

import argparse
import hashlib
import os
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path
from shutil import which
from time import monotonic

_PLUGIN_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(_PLUGIN_ROOT))

from hooks import mcp_client  # noqa: E402
from settings import CodexPluginSettings  # noqa: E402


def scope_binding_keys(cwd: str, *, session_id: str | None = None) -> list[dict[str, str]]:
    """Return session-first durable keys without transmitting the workspace path."""

    root = _git_value(cwd, "rev-parse", "--show-toplevel") or cwd
    workspace = {
        "integration": "codex",
        "kind": "workspace",
        "external_id": hashlib.sha256(root.encode("utf-8")).hexdigest(),
    }
    if session_id is None:
        return [workspace]
    if not session_id or session_id != session_id.strip() or len(session_id) > 256:
        raise ValueError("Codex session binding key is invalid")  # noqa: TRY003
    return [{"integration": "codex", "kind": "session", "external_id": session_id}, workspace]


def _git_value(cwd: str, *arguments: str) -> str | None:
    executable = which("git")
    if executable is None:
        return None
    try:
        completed = subprocess.run(  # noqa: S603 - git executable and arguments are integration-owned.
            [executable, *arguments],
            cwd=cwd,
            check=True,
            capture_output=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    value = completed.stdout
    if value.endswith(b"\r\n"):
        value = value[:-2]
    elif value.endswith(b"\n"):
        value = value[:-1]
    return value.decode("utf-8") or None


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cwd", default=os.getcwd())
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--bind-workstream", metavar="SCOPE_ID")
    action.add_argument("--clear-workstream", action="store_true")
    arguments = parser.parse_args(argv)
    settings = CodexPluginSettings()
    deadline = monotonic() + settings.http_budget_seconds
    keys = scope_binding_keys(arguments.cwd)
    workspace = keys[-1]
    if arguments.bind_workstream is not None:
        mcp_client.set_workspace_scope_binding(workspace, arguments.bind_workstream, settings=settings, deadline=deadline)
    elif arguments.clear_workstream:
        mcp_client.clear_workspace_scope_binding(workspace, settings=settings, deadline=deadline)
    print(mcp_client.resolve_scope_binding(keys, explicit_scope_id=settings.scope_id, settings=settings, deadline=deadline))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
