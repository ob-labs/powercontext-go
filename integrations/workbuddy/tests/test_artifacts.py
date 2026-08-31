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


def test_mcp_and_skill_keep_authorization_credential_free() -> None:
    mcp = json.loads((PLUGIN_ROOT / ".mcp.json").read_text(encoding="utf-8"))
    server = mcp["mcpServers"]["powercontext"]

    assert server["type"] == "http"
    assert server["url"] == "${POWERCONTEXT_WORKBUDDY_SERVER_URL:-http://127.0.0.1:8000}/mcp"
    assert server["headers"] == {"Authorization": "${POWERCONTEXT_WORKBUDDY_AUTHORIZATION:-}"}
    assert "Bearer " not in (PLUGIN_ROOT / ".mcp.json").read_text(encoding="utf-8")

    skill = (PLUGIN_ROOT / "skills" / "project-context" / "SKILL.md").read_text(encoding="utf-8")
    assert "${POWERCONTEXT_PYTHON}" in skill
    assert "${POWERCONTEXT_PROJECT_SCOPE_SCRIPT}" in skill
    assert "POWERCONTEXT_WORKBUDDY_AUTHORIZATION" not in skill
    assert (PLUGIN_ROOT / "scripts" / "__init__.py").is_file()
    assert (PLUGIN_ROOT.parents[1] / "README.md").is_file()
