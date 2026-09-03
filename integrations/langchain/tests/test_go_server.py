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

"""Process-level Go Server coverage shared by retained Python adapters."""

from __future__ import annotations

import sys
from pathlib import Path
from urllib.request import ProxyHandler, build_opener


_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(_ROOT / "test" / "retainedhost"))

from go_server import running_go_server


def test_go_server_accepts_health_request_on_loopback(tmp_path: Path) -> None:
    with running_go_server(tmp_path) as server:
        response = build_opener(ProxyHandler({})).open(f"{server.base_url}/health/ready", timeout=5)

    assert response.status == 200
