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

import json
from pathlib import Path


PLUGIN_ROOT = Path(__file__).resolve().parents[1] / "plugins" / "powercontext"


def test_setup_configuration_template_is_credential_free_and_complete() -> None:
    configuration_path = PLUGIN_ROOT / "powercontext.json.example"
    payload = json.loads(configuration_path.read_text(encoding="utf-8"))

    assert payload == {
        "schema": 1,
        "server_url": "http://127.0.0.1:8000",
        "scope_mode": "project",
        "authorization_environment": "POWERCONTEXT_WORKBUDDY_AUTHORIZATION",
        "request_timeout_seconds": 1.5,
        "request_budget_seconds": 3.0,
        "prepare_max_bytes": 8000,
        "source_max_bytes": 16384,
    }
    assert not {"authorization", "token", "password", "secret"}.intersection(payload)
