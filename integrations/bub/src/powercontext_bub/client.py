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

"""Bounded host-local HTTP client for the PowerContext Go Server."""

from __future__ import annotations

import asyncio
import ipaddress
import json
from collections.abc import Mapping
from time import monotonic
from typing import Any, Protocol, cast
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

MAX_RESPONSE_BYTES = 1_048_576
_READ_CHUNK_BYTES = 65_536
_USER_AGENT = "powercontext-bub-plugin/0.1.0"


class InvalidResponseError(RuntimeError):
    """The Server returned bytes outside the expected public contract."""


class TransportError(RuntimeError):
    """The Server could not be reached within the configured budget."""


class ServerResponseError(RuntimeError):
    """The Server returned a declared non-success response."""

    def __init__(self, status: int, request_id: str | None) -> None:
        self.status = status
        self.request_id = request_id
        super().__init__(f"PowerContext Server returned HTTP {status}")


class _Response(Protocol):
    fp: object
    status: int
    headers: Mapping[str, str]

    def __enter__(self) -> _Response: ...

    def __exit__(self, *args: object) -> object: ...

    def read(self, amount: int = -1) -> bytes: ...


class _RejectRedirects(HTTPRedirectHandler):
    def redirect_request(
        self,
        req: Request,
        fp: object,
        code: int,
        msg: str,
        headers: object,
        newurl: str,
    ) -> Request | None:
        del req, fp, code, msg, headers, newurl
        return None


_URL_OPENER = build_opener(_RejectRedirects)


class PowerContextHTTPClient:
    """The narrow five-operation client required by the Bub integration."""

    def __init__(
        self,
        base_url: str,
        *,
        token: str | None = None,
        timeout: float = 10.0,
        trust_transport_security: bool = False,
    ) -> None:
        self._base_url = _http_base_url(base_url, trust_transport_security=trust_transport_security)
        if timeout <= 0:
            raise ValueError("PowerContext timeout must be positive")
        if token is not None and (not token or any(character.isspace() for character in token)):
            raise ValueError("PowerContext API token must be a non-empty bearer token")
        self._timeout = timeout
        self._token = token

    async def post(self, path: str, payload: Mapping[str, object], *, expected_status: int) -> dict[str, Any]:
        if not path.startswith("/") or path.startswith("//") or "?" in path or "#" in path:
            raise ValueError("PowerContext operation path is invalid")
        try:
            return await asyncio.wait_for(
                asyncio.to_thread(self._post, path, payload, expected_status),
                timeout=self._timeout,
            )
        except (TimeoutError, asyncio.TimeoutError) as error:
            raise TransportError("PowerContext Server request timed out") from error

    def _post(self, path: str, payload: Mapping[str, object], expected_status: int) -> dict[str, Any]:
        try:
            body = json.dumps(payload, ensure_ascii=True, separators=(",", ":")).encode("utf-8")
        except (TypeError, ValueError) as error:
            raise InvalidResponseError("PowerContext request is not JSON-compatible") from error
        headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": _USER_AGENT,
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        request = Request(self._base_url + path, data=body, headers=headers, method="POST")  # noqa: S310
        deadline = monotonic() + self._timeout
        try:
            with cast(_Response, _URL_OPENER.open(request, timeout=self._timeout)) as response:
                if response.status != expected_status:
                    raise ServerResponseError(response.status, response.headers.get("X-PowerContext-Request-ID"))
                encoded = _read_response(response, deadline)
        except HTTPError as error:
            raise ServerResponseError(error.code, error.headers.get("X-PowerContext-Request-ID")) from error
        except (OSError, URLError) as error:
            raise TransportError("PowerContext Server is unavailable") from error
        try:
            value = json.loads(encoded)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise InvalidResponseError("PowerContext Server returned invalid JSON") from error
        if not isinstance(value, dict):
            raise InvalidResponseError("PowerContext Server response must be an object")
        return cast(dict[str, Any], value)


def validate_prepared_context(value: Mapping[str, object], *, max_bytes: int) -> dict[str, object]:
    fields = {"schema", "status", "content", "content_bytes"}
    if set(value) != fields or value.get("schema") != "powercontext.prepared-context.v1":
        raise InvalidResponseError("prepared context schema is invalid")
    status, content, content_bytes = value["status"], value["content"], value["content_bytes"]
    if not isinstance(content_bytes, int) or isinstance(content_bytes, bool) or content_bytes < 0:
        raise InvalidResponseError("prepared context byte count is invalid")
    if status == "empty":
        if content is not None or content_bytes != 0:
            raise InvalidResponseError("empty prepared context is inconsistent")
    elif status == "ready":
        if not isinstance(content, str) or not content.strip():
            raise InvalidResponseError("ready prepared context has no content")
        if len(content.encode("utf-8")) != content_bytes or content_bytes > max_bytes:
            raise InvalidResponseError("prepared context exceeds its byte contract")
    else:
        raise InvalidResponseError("prepared context status is invalid")
    return dict(value)


def require_int(value: object, field: str, *, minimum: int = 0) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < minimum:
        raise InvalidResponseError(f"PowerContext response field {field} is invalid")
    return value


def require_list(value: object, field: str) -> list[Any]:
    if not isinstance(value, list):
        raise InvalidResponseError(f"PowerContext response field {field} is invalid")
    return value


def _read_response(response: _Response, deadline: float) -> bytes:
    chunks: list[bytes] = []
    total = 0
    while True:
        _set_response_timeout(response, _remaining_time(deadline))
        chunk = response.read(min(_READ_CHUNK_BYTES, MAX_RESPONSE_BYTES + 1 - total))
        if not chunk:
            return b"".join(chunks)
        total += len(chunk)
        if total > MAX_RESPONSE_BYTES:
            raise InvalidResponseError("PowerContext Server response exceeds 1 MiB")
        chunks.append(chunk)


def _remaining_time(deadline: float) -> float:
    remaining = deadline - monotonic()
    if remaining <= 0:
        raise TransportError("PowerContext Server request timed out")
    return remaining


def _set_response_timeout(response: _Response, timeout: float) -> None:
    raw = getattr(response.fp, "raw", None)
    sock = getattr(raw, "_sock", None)
    settimeout = getattr(sock, "settimeout", None)
    if settimeout is not None:
        settimeout(timeout)


def _http_base_url(value: str, *, trust_transport_security: bool = False) -> str:
    parsed = urlsplit(value.rstrip("/"))
    if parsed.scheme not in {"http", "https"} or parsed.hostname is None:
        raise ValueError("PowerContext base URL must use HTTP or HTTPS")
    if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        raise ValueError("PowerContext base URL must not contain credentials, query data, or a fragment")
    if parsed.scheme == "http" and not trust_transport_security and not _is_loopback_host(parsed.hostname):
        raise ValueError("unencrypted PowerContext URLs must be loopback addresses")
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip("/"), "", ""))


def _is_loopback_host(host: str) -> bool:
    normalized = host.strip().lower()
    if normalized == "localhost":
        return True
    try:
        return ipaddress.ip_address(normalized).is_loopback
    except ValueError:
        return False


__all__ = [
    "InvalidResponseError",
    "PowerContextHTTPClient",
    "ServerResponseError",
    "TransportError",
    "require_int",
    "require_list",
    "validate_prepared_context",
]
