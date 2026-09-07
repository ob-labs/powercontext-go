#!/usr/bin/env python3
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

"""A bounded Streamable MCP client for the Codex prompt hook."""

from __future__ import annotations

import json
from collections.abc import Mapping
from contextlib import suppress
from time import monotonic
from typing import Protocol, cast
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

from settings import CodexPluginSettings, _is_loopback_host
from typing_extensions import override

_MAX_RESPONSE_BYTES = 1_048_576
_READ_CHUNK_BYTES = 65_536
_PROTOCOL_VERSION = "2025-11-25"


class MCPResolutionError(RuntimeError):
    """Raised when a scoped MCP tool call cannot produce a safe Scope."""


class _Response(Protocol):
    def read(self, amount: int = -1) -> bytes: ...


class _RejectRedirects(HTTPRedirectHandler):
    @override
    def redirect_request(
        self,
        req: Request,
        fp: object,
        code: int,
        msg: str,
        headers: object,
        newurl: str,
    ) -> Request | None:
        return None


class _LoopbackAwareProxyHandler(ProxyHandler):
    @override
    def proxy_open(self, req: Request, proxy: str, type: str) -> object | None:
        if _is_loopback_host(urlsplit(req.full_url).hostname or ""):
            return None
        return super().proxy_open(req, proxy, type)


_URL_OPENER = build_opener(_RejectRedirects, _LoopbackAwareProxyHandler())


def resolve_scope_binding(
    binding_keys: list[dict[str, str]],
    *,
    explicit_scope_id: str | None,
    settings: CodexPluginSettings,
    deadline: float,
) -> str:
    """Resolve one durable Scope through the configured MCP endpoint."""

    arguments: dict[str, object] = {"binding_keys": binding_keys}
    if explicit_scope_id is not None:
        arguments["explicit_scope_id"] = explicit_scope_id
    result = _call_tool(
        "scope_binding_resolve", arguments, settings=settings, deadline=deadline
    )
    scope_id = result.get("scope_id")
    if (
        not isinstance(scope_id, str)
        or not scope_id.strip()
        or scope_id != scope_id.strip()
    ):
        raise MCPResolutionError
    return scope_id


def set_workspace_scope_binding(
    key: dict[str, str],
    scope_id: str,
    *,
    settings: CodexPluginSettings,
    deadline: float,
) -> None:
    _call_tool(
        "scope_binding_set",
        {**key, "scope_id": scope_id},
        settings=settings,
        deadline=deadline,
    )


def clear_workspace_scope_binding(
    key: dict[str, str],
    *,
    settings: CodexPluginSettings,
    deadline: float,
) -> None:
    _call_tool("scope_binding_clear", key, settings=settings, deadline=deadline)


def _call_tool(
    name: str,
    arguments: Mapping[str, object],
    *,
    settings: CodexPluginSettings,
    deadline: float,
) -> Mapping[str, object]:
    session_id: str | None = None
    protocol_version = _PROTOCOL_VERSION
    try:
        initialized, headers = _request(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": _PROTOCOL_VERSION,
                    "capabilities": {},
                    "clientInfo": {
                        "name": "powercontext-codex-hook",
                        "version": "0.2.0",
                    },
                },
            },
            settings=settings,
            deadline=deadline,
            session_id=None,
            protocol_version=None,
            allow_empty=False,
        )
        result = _result(initialized, 1)
        version = result.get("protocolVersion")
        if not isinstance(version, str) or not version:
            raise MCPResolutionError
        protocol_version = version
        session_id = _header(headers, "Mcp-Session-Id")
        if session_id is not None and (not session_id or len(session_id) > 256):
            raise MCPResolutionError
        _request(
            {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
            settings=settings,
            deadline=deadline,
            session_id=session_id,
            protocol_version=protocol_version,
            allow_empty=True,
        )
        response, _ = _request(
            {
                "jsonrpc": "2.0",
                "id": 2,
                "method": "tools/call",
                "params": {"name": name, "arguments": arguments},
            },
            settings=settings,
            deadline=deadline,
            session_id=session_id,
            protocol_version=protocol_version,
            allow_empty=False,
        )
        tool_result = _result(response, 2)
        if tool_result.get("isError") is True:
            raise MCPResolutionError
        structured = tool_result.get("structuredContent")
        if not isinstance(structured, dict):
            raise MCPResolutionError
        return cast(dict[str, object], structured)
    except (
        HTTPError,
        OSError,
        TimeoutError,
        ValueError,
        TypeError,
        KeyError,
        json.JSONDecodeError,
    ) as error:
        raise MCPResolutionError from error
    finally:
        if session_id:
            with suppress(Exception):
                _close(settings, deadline, session_id, protocol_version)


def _request(
    payload: Mapping[str, object],
    *,
    settings: CodexPluginSettings,
    deadline: float,
    session_id: str | None,
    protocol_version: str | None,
    allow_empty: bool,
) -> tuple[dict[str, object], Mapping[str, str]]:
    headers = _headers(settings)
    if protocol_version is not None:
        headers["Mcp-Protocol-Version"] = protocol_version
    if session_id is not None:
        headers["Mcp-Session-Id"] = session_id
    request = Request(
        settings.mcp_url,
        data=json.dumps(payload, separators=(",", ":")).encode("utf-8"),
        headers=headers,
        method="POST",
    )
    timeout = min(settings.request_timeout_seconds, _remaining_time(deadline))
    try:
        with _URL_OPENER.open(request, timeout=timeout) as response:
            if response.status == 202 and allow_empty:
                return {}, dict(response.headers.items())
            if response.status != 200:
                raise MCPResolutionError
            raw = _read_response(response, deadline=deadline)
            return _decode_response(
                raw, response.headers.get("Content-Type", "")
            ), dict(response.headers.items())
    except HTTPError as error:
        raise MCPResolutionError from error


def _close(
    settings: CodexPluginSettings,
    deadline: float,
    session_id: str,
    protocol_version: str,
) -> None:
    request = Request(
        settings.mcp_url,
        headers={
            **_headers(settings),
            "Mcp-Protocol-Version": protocol_version,
            "Mcp-Session-Id": session_id,
        },
        method="DELETE",
    )
    timeout = min(settings.request_timeout_seconds, _remaining_time(deadline))
    with _URL_OPENER.open(request, timeout=timeout):
        pass


def _headers(settings: CodexPluginSettings) -> dict[str, str]:
    headers = {
        "Accept": "application/json, text/event-stream",
        "Content-Type": "application/json",
        "User-Agent": "powercontext-codex-plugin/0.2.0",
    }
    if settings.authorization is not None:
        headers["Authorization"] = settings.authorization.get_secret_value()
    return headers


def _header(headers: Mapping[str, str], name: str) -> str | None:
    expected = name.casefold()
    for key, value in headers.items():
        if key.casefold() == expected:
            return value
    return None


def _result(response: Mapping[str, object], request_id: int) -> Mapping[str, object]:
    if (
        response.get("jsonrpc") != "2.0"
        or response.get("id") != request_id
        or "error" in response
    ):
        raise MCPResolutionError
    result = response.get("result")
    if not isinstance(result, dict):
        raise MCPResolutionError
    return cast(dict[str, object], result)


def _decode_response(raw: bytes, content_type: str) -> dict[str, object]:
    if "application/json" in content_type:
        decoded = json.loads(raw)
    elif "text/event-stream" in content_type:
        events = [
            line.removeprefix(b"data:").lstrip()
            for line in raw.splitlines()
            if line.startswith(b"data:")
        ]
        if len(events) != 1:
            raise MCPResolutionError
        decoded = json.loads(events[0])
    else:
        raise MCPResolutionError
    if not isinstance(decoded, dict):
        raise MCPResolutionError
    return cast(dict[str, object], decoded)


def _read_response(response: _Response, *, deadline: float) -> bytes:
    content = bytearray()
    while True:
        _set_response_timeout(response, _remaining_time(deadline))
        chunk = response.read(
            min(_READ_CHUNK_BYTES, _MAX_RESPONSE_BYTES + 1 - len(content))
        )
        if not chunk:
            return bytes(content)
        content.extend(chunk)
        if len(content) > _MAX_RESPONSE_BYTES:
            raise MCPResolutionError


def _set_response_timeout(response: object, timeout: float) -> None:
    raw = getattr(getattr(response, "fp", None), "raw", None)
    sock = getattr(raw, "_sock", None)
    settimeout = getattr(sock, "settimeout", None)
    if settimeout is not None:
        settimeout(timeout)


def _remaining_time(deadline: float) -> float:
    remaining = deadline - monotonic()
    if remaining <= 0:
        raise TimeoutError
    return remaining
