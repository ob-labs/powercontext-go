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
import re
import tomllib
from importlib.metadata import distribution
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
REQUIREMENT_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*")


def test_source_checkout_install_uses_local_adapter_without_python_sdk_override() -> None:
    direct_url_payload = distribution("powercontext-bub").read_text("direct_url.json")
    assert direct_url_payload is not None
    direct_url = json.loads(direct_url_payload)
    assert direct_url["url"].rstrip("/") == PROJECT_ROOT.as_uri()
    assert direct_url["dir_info"]["editable"] is True

    project = tomllib.loads((PROJECT_ROOT / "pyproject.toml").read_text(encoding="utf-8"))
    dependency_names = {_requirement_name(requirement) for requirement in project["project"]["dependencies"]}
    assert "powercontext" not in dependency_names


def _requirement_name(requirement: str) -> str:
    match = REQUIREMENT_NAME.match(requirement)
    assert match is not None, f"invalid requirement {requirement!r}"
    return match.group().lower().replace("_", "-")
