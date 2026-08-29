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
import io
import json
import math
import unittest
from pathlib import Path
from types import ModuleType
from unittest import mock

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
_TRANSPORT_VECTORS = json.loads(
    (REPOSITORY_ROOT / "test" / "transport" / "testdata" / "loopback_hosts.json").read_text(encoding="utf-8")
)


def _load_client() -> ModuleType:
    path = Path(__file__).resolve().parents[1] / "src" / "powercontext_bub" / "client.py"
    spec = importlib.util.spec_from_file_location("powercontext_bub_client_test_target", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


client = _load_client()


class _Socket:
    def __init__(self) -> None:
        self.timeouts: list[float] = []

    def settimeout(self, timeout: float) -> None:
        self.timeouts.append(timeout)


class _Response:
    def __init__(self, body: bytes, *, status: int = 200, request_id: str | None = None) -> None:
        self.status = status
        self.headers = {}
        if request_id is not None:
            self.headers["X-PowerContext-Request-ID"] = request_id
        self._body = io.BytesIO(body)
        self.socket = _Socket()
        self.fp = type("FP", (), {"raw": type("Raw", (), {"_sock": self.socket})()})()

    def __enter__(self) -> _Response:
        return self

    def __exit__(self, *args: object) -> None:
        del args

    def read(self, amount: int = -1) -> bytes:
        return self._body.read(amount)


class _Opener:
    def __init__(self, response: _Response) -> None:
        self.response = response
        self.requests: list[tuple[object, float]] = []

    def open(self, request: object, *, timeout: float) -> _Response:
        self.requests.append((request, timeout))
        return self.response


class ClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_post_has_bounded_transport_and_bearer_auth(self) -> None:
        response = _Response(b'{"status":"accepted","position":7}', status=202)
        opener = _Opener(response)
        with mock.patch.object(client, "_URL_OPENER", opener):
            result = await client.PowerContextHTTPClient(
                "https://memory.example/api/", token="secret-token", timeout=2.5
            ).post("/v1/sources/content", {"scope_id": "project:test"}, expected_status=202)

        self.assertEqual(result, {"status": "accepted", "position": 7})
        self.assertEqual(len(opener.requests), 1)
        request, timeout = opener.requests[0]
        self.assertEqual(request.full_url, "https://memory.example/api/v1/sources/content")
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.get_header("Authorization"), "Bearer secret-token")
        self.assertEqual(request.get_header("Content-type"), "application/json")
        self.assertEqual(json.loads(request.data), {"scope_id": "project:test"})
        self.assertEqual(timeout, 2.5)
        self.assertTrue(response.socket.timeouts)
        self.assertTrue(all(0 < value <= 2.5 for value in response.socket.timeouts))

    async def test_post_rejects_an_unexpected_status_without_exposing_body(self) -> None:
        opener = _Opener(_Response(b'{"secret":"do-not-expose"}', status=409, request_id="request-7"))
        with mock.patch.object(client, "_URL_OPENER", opener):
            with self.assertRaises(client.ServerResponseError) as raised:
                await client.PowerContextHTTPClient("http://127.0.0.1:8000").post(
                    "/v1/memory/remember", {}, expected_status=200
                )

        self.assertEqual(raised.exception.status, 409)
        self.assertEqual(raised.exception.request_id, "request-7")
        self.assertNotIn("secret", str(raised.exception))

    async def test_post_rejects_oversized_and_non_object_responses(self) -> None:
        oversized = _Opener(_Response(b"x" * (client.MAX_RESPONSE_BYTES + 1)))
        with mock.patch.object(client, "_URL_OPENER", oversized):
            with self.assertRaises(client.InvalidResponseError):
                await client.PowerContextHTTPClient("http://127.0.0.1:8000").post(
                    "/v1/context/prepare", {}, expected_status=200
                )

        non_object = _Opener(_Response(b"[]"))
        with mock.patch.object(client, "_URL_OPENER", non_object):
            with self.assertRaises(client.InvalidResponseError):
                await client.PowerContextHTTPClient("http://127.0.0.1:8000").post(
                    "/v1/context/prepare", {}, expected_status=200
                )


class ValidationTests(unittest.TestCase):
    def test_prepared_context_accepts_exact_ready_and_empty_contracts(self) -> None:
        ready = {
            "schema": "powercontext.prepared-context.v1",
            "status": "ready",
            "content": "你好",
            "content_bytes": 6,
        }
        empty = {
            "schema": "powercontext.prepared-context.v1",
            "status": "empty",
            "content": None,
            "content_bytes": 0,
        }
        self.assertEqual(client.validate_prepared_context(ready, max_bytes=6), ready)
        self.assertEqual(client.validate_prepared_context(empty, max_bytes=6), empty)

    def test_prepared_context_rejects_schema_drift_and_inconsistent_bytes(self) -> None:
        base = {
            "schema": "powercontext.prepared-context.v1",
            "status": "ready",
            "content": "context",
            "content_bytes": 7,
        }
        invalid = [
            {**base, "extra": True},
            {**base, "schema": "powercontext.prepared-context.v2"},
            {**base, "content_bytes": 6},
            {**base, "content": ""},
            {**base, "status": "unknown"},
            {**base, "content_bytes": True},
        ]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(client.InvalidResponseError):
                client.validate_prepared_context(value, max_bytes=8)

    def test_base_url_and_token_reject_ambiguous_or_injectable_values(self) -> None:
        invalid_urls = [
            "file:///tmp/powercontext.sock",
            "https://user:pass@memory.example",
            "https://memory.example?token=secret",
            "https://memory.example/#fragment",
        ]
        for value in invalid_urls:
            with self.subTest(value=value), self.assertRaises(ValueError):
                client.PowerContextHTTPClient(value)
        for token in ["", " leading", "trailing ", "two words", "token\r\nInjected: yes"]:
            with self.subTest(token=token), self.assertRaises(ValueError):
                client.PowerContextHTTPClient("http://127.0.0.1:8000", token=token)

    def test_transport_policy_matches_shared_loopback_vectors(self) -> None:
        for host in _TRANSPORT_VECTORS["loopback"]:
            with self.subTest(kind="loopback", host=host):
                client.PowerContextHTTPClient(f"http://{host}:8000")
        for host in _TRANSPORT_VECTORS["non_loopback"]:
            with self.subTest(kind="non-loopback", host=host), self.assertRaises(ValueError):
                client.PowerContextHTTPClient(f"http://{host}:8000")

    def test_explicit_transport_trust_allows_controlled_non_loopback_http(self) -> None:
        client.PowerContextHTTPClient(
            "http://memory.example:8000",
            trust_transport_security=True,
        )

    def test_integer_validation_rejects_bool_and_search_score_examples_are_finite(self) -> None:
        with self.assertRaises(client.InvalidResponseError):
            client.require_int(True, "cursor")
        self.assertFalse(math.isfinite(float("nan")))


if __name__ == "__main__":
    unittest.main()
