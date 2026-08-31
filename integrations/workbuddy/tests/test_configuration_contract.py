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

import pytest


PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"


def load_settings_module() -> ModuleType:
    path = PLUGIN_ROOT / "hooks" / "workbuddy_settings.py"
    spec = importlib.util.spec_from_file_location("powercontext_workbuddy_settings_contract", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def configuration() -> dict[str, object]:
    return {
        "schema": 1,
        "server_url": "http://127.0.0.1:8000",
        "scope_mode": "project",
        "authorization_environment": "WORKBUDDY_TOKEN",
        "request_timeout_seconds": 1.5,
        "request_budget_seconds": 3.0,
        "prepare_max_bytes": 4096,
        "source_max_bytes": 16384,
    }


def test_config_declares_bounded_request_limits(tmp_path: Path) -> None:
    settings_module = load_settings_module()
    configuration_path = tmp_path / "powercontext.json"
    configuration_path.write_text(json.dumps(configuration()), encoding="utf-8")

    settings = settings_module.load_settings(configuration_path)

    assert settings.request_timeout_seconds == 1.5
    assert settings.request_budget_seconds == 3.0
    assert settings.prepare_max_bytes == 4096
    assert settings.source_max_bytes == 16384


def test_config_accepts_the_ipv4_loopback_range(tmp_path: Path) -> None:
    settings_module = load_settings_module()
    payload = configuration()
    payload["server_url"] = "http://127.2.3.4:8000/"
    configuration_path = tmp_path / "powercontext.json"
    configuration_path.write_text(json.dumps(payload), encoding="utf-8")

    settings = settings_module.load_settings(configuration_path)

    assert settings.server_url == "http://127.2.3.4:8000"


@pytest.mark.parametrize(
    ("changes", "expected_message"),
    [
        ({"server_url": "http://198.51.100.4:8000"}, "loopback"),
        ({"request_timeout_seconds": 0}, "timeout"),
        ({"request_timeout_seconds": 4.0}, "budget"),
        ({"prepare_max_bytes": 0}, "prepare"),
        ({"source_max_bytes": 65537}, "source"),
        ({"authorization_environment": "授权"}, "authorization environment"),
    ],
)
def test_config_rejects_unsafe_or_unbounded_runtime_limits(
    tmp_path: Path,
    changes: dict[str, object],
    expected_message: str,
) -> None:
    settings_module = load_settings_module()
    payload = configuration()
    payload.update(changes)
    configuration_path = tmp_path / "powercontext.json"
    configuration_path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(settings_module.WorkBuddyConfigurationError, match=expected_message):
        settings_module.load_settings(configuration_path)


def test_config_rejects_persisted_authorization_without_echoing_it(tmp_path: Path) -> None:
    settings_module = load_settings_module()
    persisted_secret = "Bearer persisted-workbuddy-secret"
    payload = configuration()
    payload["authorization"] = persisted_secret
    configuration_path = tmp_path / "powercontext.json"
    configuration_path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(settings_module.WorkBuddyConfigurationError) as raised:
        settings_module.load_settings(configuration_path)

    assert persisted_secret not in str(raised.value)
