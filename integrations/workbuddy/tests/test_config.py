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
import sys
from pathlib import Path
from types import ModuleType


PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"


def load_config_module() -> ModuleType:
    path = PLUGIN_ROOT / "hooks" / "workbuddy_settings.py"
    spec = importlib.util.spec_from_file_location("powercontext_workbuddy_settings", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_config_uses_credential_free_file_and_runtime_authorization_environment(tmp_path: Path) -> None:
    settings_module = load_config_module()
    configuration_path = tmp_path / "powercontext.json"
    configuration_path.write_text(
        json.dumps(
            {
                "schema": 1,
                "server_url": "http://127.0.0.1:8000",
                "scope_mode": "project",
                "authorization_environment": "WORKBUDDY_TOKEN",
                "request_timeout_seconds": 1.5,
                "request_budget_seconds": 3.0,
                "prepare_max_bytes": 8192,
                "source_max_bytes": 16384,
            }
        ),
        encoding="utf-8",
    )
    configuration = settings_module.load_settings(
        configuration_path,
        environment={"WORKBUDDY_TOKEN": "Bearer test-token"},
    )

    assert configuration.server_url == "http://127.0.0.1:8000"
    assert configuration.authorization == "Bearer test-token"
    assert configuration.authorization_environment == "WORKBUDDY_TOKEN"
    assert "test-token" not in configuration_path.read_text(encoding="utf-8")
