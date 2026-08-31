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
import sys
from pathlib import Path
from types import ModuleType


PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"


def load_scope_module() -> ModuleType:
    path = PLUGIN_ROOT / "scripts" / "project_scope.py"
    spec = importlib.util.spec_from_file_location("powercontext_workbuddy_scope", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_project_scope_uses_normalized_git_remote_for_cross_host_recall(monkeypatch) -> None:
    scope = load_scope_module()

    def git_value(_cwd: str, *arguments: str) -> str | None:
        if arguments == ("rev-parse", "--show-toplevel"):
            return r"C:\work\powercontext-go"
        if arguments == ("config", "--get", "remote.origin.url"):
            return "git@github.com:ob-labs/powercontext-go.git"
        return None

    monkeypatch.setattr(scope, "_git_value", git_value)

    assert scope.resolve_scope_id(r"C:\work\powercontext-go") == "git:github.com/ob-labs/powercontext-go"


def test_agent_scope_does_not_depend_on_the_checkout_path() -> None:
    scope = load_scope_module()

    assert scope.resolve_scope_id(r"C:\one", scope_mode="agent") == "workbuddy:agent"
    assert scope.resolve_scope_id(r"D:\two", scope_mode="agent") == "workbuddy:agent"
