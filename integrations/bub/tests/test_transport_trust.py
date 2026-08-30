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

from pathlib import Path
from types import SimpleNamespace

import pytest
from powercontext_bub import plugin as plugin_module
from powercontext_bub import tools as tools_module
from powercontext_bub.client import PowerContextHTTPClient
from powercontext_bub.plugin import STATE_KEY, PowerContextPlugin, PowerContextSettings


def _plugin_with(
    settings: PowerContextSettings,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> PowerContextPlugin:
    monkeypatch.setattr(plugin_module, "ensure_config", lambda _: settings)
    return PowerContextPlugin(SimpleNamespace(workspace=tmp_path))


def _tool_settings(
    plugin: PowerContextPlugin,
    settings: PowerContextSettings,
    monkeypatch: pytest.MonkeyPatch,
) -> tools_module.ToolSettings:
    monkeypatch.setattr(tools_module, "ensure_config", lambda _: settings)
    state = plugin.load_state(message=None, session_id="session-1")
    return tools_module._settings(SimpleNamespace(state=state))


def test_plugin_refuses_a_plaintext_non_loopback_server_by_default(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = PowerContextSettings(base_url="http://host-gateway:8000", scope_id="test:scope")
    plugin = _plugin_with(settings, monkeypatch, tmp_path)

    with pytest.raises(ValueError, match="loopback"):
        plugin._client()


def test_trusting_the_transport_supplies_the_client_an_explicit_vouched_transport(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = PowerContextSettings(
        base_url="http://host-gateway:8000",
        scope_id="test:scope",
        trust_transport_security=True,
    )
    plugin = _plugin_with(settings, monkeypatch, tmp_path)

    assert isinstance(plugin._client(), PowerContextHTTPClient)


def test_load_state_carries_the_transport_vouch_to_tool_settings(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    refusing = _plugin_with(
        PowerContextSettings(base_url="http://host-gateway:8000", scope_id="test:scope"),
        monkeypatch,
        tmp_path,
    )
    assert refusing.load_state(message=None, session_id="session-1")[STATE_KEY]["trust_transport_security"] is False

    vouched = _plugin_with(
        PowerContextSettings(
            base_url="http://host-gateway:8000",
            scope_id="test:scope",
            trust_transport_security=True,
        ),
        monkeypatch,
        tmp_path,
    )
    assert vouched.load_state(message=None, session_id="session-1")[STATE_KEY]["trust_transport_security"] is True


def test_tools_refuse_a_plaintext_non_loopback_server_by_default(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = PowerContextSettings(base_url="http://host-gateway:8000", scope_id="test:scope")
    plugin = _plugin_with(settings, monkeypatch, tmp_path)

    with pytest.raises(ValueError, match="loopback"):
        tools_module._client(_tool_settings(plugin, settings, monkeypatch))


def test_tools_honour_the_operator_transport_vouch(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = PowerContextSettings(
        base_url="http://host-gateway:8000",
        scope_id="test:scope",
        trust_transport_security=True,
    )
    plugin = _plugin_with(settings, monkeypatch, tmp_path)

    client = tools_module._client(_tool_settings(plugin, settings, monkeypatch))

    assert isinstance(client, PowerContextHTTPClient)
