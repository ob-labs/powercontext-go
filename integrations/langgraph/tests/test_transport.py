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

import asyncio
import json
from pathlib import Path

import httpx
import pytest

pytest.importorskip("powercontext_langgraph")

from powercontext_langgraph.client import (
    PowerContextClient,
    ResolvedConfig,
    open_client,
    shared_http_client,
)

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
_TRANSPORT_VECTORS = json.loads(
    (REPOSITORY_ROOT / "test" / "transport" / "testdata" / "loopback_hosts.json").read_text(encoding="utf-8")
)


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["loopback"])
def test_client_accepts_shared_loopback_hosts(host: str) -> None:
    client = PowerContextClient(f"http://{host}:8000")
    asyncio.run(client.aclose())


@pytest.mark.parametrize("host", _TRANSPORT_VECTORS["non_loopback"])
def test_client_rejects_shared_non_loopback_plaintext_hosts(host: str) -> None:
    with pytest.raises(ValueError):
        PowerContextClient(f"http://{host}:8000")


def test_client_requires_an_explicit_vouch_for_a_caller_supplied_transport() -> None:
    transport = httpx.AsyncClient(transport=httpx.MockTransport(lambda _request: httpx.Response(200)))
    try:
        with pytest.raises(ValueError):
            PowerContextClient("http://memory.example:8000", http_client=transport)
        client = PowerContextClient(
            "http://memory.example:8000",
            http_client=transport,
            trust_transport_security=True,
        )
        asyncio.run(client.aclose())
    finally:
        asyncio.run(transport.aclose())


def test_shared_http_client_keeps_transport_untrusted_by_default() -> None:
    transport = httpx.AsyncClient(transport=httpx.MockTransport(lambda _request: httpx.Response(200)))
    config = ResolvedConfig(
        base_url="http://memory.example:8000",
        scope_id="project:transport-policy",
        token=None,
        timeout=1.0,
        max_bytes=8000,
    )
    try:
        with shared_http_client(transport), pytest.raises(ValueError):
            open_client(config)
        with shared_http_client(transport, trust_transport_security=True):
            client = open_client(config)
            asyncio.run(client.aclose())
    finally:
        asyncio.run(transport.aclose())
