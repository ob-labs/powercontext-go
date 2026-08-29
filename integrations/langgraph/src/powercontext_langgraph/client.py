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

"""Bounded host-local client for the PowerContext Go Server.

The LangGraph adapter is distributed independently and deliberately does not
depend on the retired public Python SDK. This module implements only the three
HTTP operations the adapter consumes.
"""

from __future__ import annotations

import asyncio
import ipaddress
import json
import math
from collections.abc import Iterator
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any, Self
from urllib.parse import urlsplit, urlunsplit

import httpx
from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    SecretStr,
    ValidationError,
    field_validator,
    model_validator,
)

from .scope import PowerContextScope, resolve_scope_id
from .settings import PowerContextLangGraphSettings

MAX_RESPONSE_BYTES = 1_048_576
_USER_AGENT = "powercontext-langgraph/0.0.1"

# A shared HTTP client lets a long-running deployment reuse one connection pool across nodes and tools, and lets
# tests provide an in-process transport. Per-operation clients borrow it and never close it.
_SHARED_HTTP_CLIENT: ContextVar[tuple[httpx.AsyncClient, bool] | None] = ContextVar(
    "powercontext_langgraph_http_client", default=None
)


class ClientError(RuntimeError):
    """Base class for declared PowerContext client failures."""


class TransportError(ClientError):
    """The Server could not be reached inside the operation budget."""


class InvalidResponseError(ClientError):
    """The Server returned bytes outside the consumed public contract."""


class ServerResponseError(ClientError):
    """The Server returned a non-success status."""

    def __init__(self, status_code: int, request_id: str | None) -> None:
        self.status_code = status_code
        self.request_id = request_id
        super().__init__(f"PowerContext Server returned HTTP {status_code}")


class _WireModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


def _non_blank(value: str, *, maximum: int, field_name: str) -> str:
    if not value or len(value) > maximum or not value.strip():
        raise ValueError(
            f"{field_name} must be non-blank and no longer than {maximum} characters"
        )
    return value


class PrepareContextRequest(_WireModel):
    scope_id: str
    query: str
    max_bytes: int = Field(default=8000, ge=512, le=32768)

    @field_validator("scope_id")
    @classmethod
    def _scope(cls, value: str) -> str:
        return _non_blank(value, maximum=256, field_name="scope_id")

    @field_validator("query")
    @classmethod
    def _query(cls, value: str) -> str:
        return _non_blank(value, maximum=8192, field_name="query")


class SearchMemoryRequest(_WireModel):
    scope_id: str
    query: str
    limit: int = Field(default=10, ge=1, le=50)

    @field_validator("scope_id")
    @classmethod
    def _scope(cls, value: str) -> str:
        return _non_blank(value, maximum=256, field_name="scope_id")

    @field_validator("query")
    @classmethod
    def _query(cls, value: str) -> str:
        return _non_blank(value, maximum=8192, field_name="query")


class RememberMemoryRequest(_WireModel):
    scope_id: str
    kind: str
    text: str
    reason: str | None = Field(default=None, max_length=512)

    @field_validator("scope_id")
    @classmethod
    def _scope(cls, value: str) -> str:
        return _non_blank(value, maximum=256, field_name="scope_id")

    @field_validator("kind")
    @classmethod
    def _kind(cls, value: str) -> str:
        if not value or len(value) > 128:
            raise ValueError("kind must contain 1 to 128 characters")
        return value

    @field_validator("text")
    @classmethod
    def _text(cls, value: str) -> str:
        if not value or len(value.encode("utf-8")) > 8192:
            raise ValueError("text must contain 1 to 8192 UTF-8 bytes")
        return value


class PreparedContextStatus(StrEnum):
    READY = "ready"
    EMPTY = "empty"


class PreparedContext(_WireModel):
    schema_: str = Field(alias="schema")
    status: PreparedContextStatus
    content: str | None
    content_bytes: int = Field(ge=0)

    @model_validator(mode="after")
    def _consistent(self) -> Self:
        if self.schema_ != "powercontext.prepared-context.v1":
            raise ValueError("prepared context schema is invalid")
        if self.status is PreparedContextStatus.EMPTY:
            if self.content is not None or self.content_bytes != 0:
                raise ValueError("empty prepared context is inconsistent")
        elif self.content is None or not self.content.strip():
            raise ValueError("ready prepared context has no content")
        elif len(self.content.encode("utf-8")) != self.content_bytes:
            raise ValueError("prepared context byte count is inconsistent")
        return self


class MemoryMatchedBy(StrEnum):
    FTS = "fts"
    VECTOR = "vector"


class ArtifactReference(_WireModel):
    family: str
    artifact_id: str
    revision: int = Field(ge=1)


class SourceReference(_WireModel):
    name: str
    source_id: str


class MemoryCitation(_WireModel):
    memory_ref: ArtifactReference
    entry_id: str
    entry_version_id: str


class MemoryEntry(_WireModel):
    citation: MemoryCitation
    version: int = Field(ge=1)
    kind: str
    text: str
    state: str
    source_refs: list[SourceReference]
    artifact_refs: list[ArtifactReference]


class MemoryMutationResponse(_WireModel):
    memory: ArtifactReference
    entry: MemoryEntry | None = None


class SearchMemoryHit(_WireModel):
    citation: MemoryCitation
    text: str
    score: float = Field(ge=0, le=1)
    matched_by: list[MemoryMatchedBy]

    @field_validator("score")
    @classmethod
    def _finite_score(cls, value: float) -> float:
        if not math.isfinite(value):
            raise ValueError("memory score must be finite")
        return value


class SearchMemoryResponse(_WireModel):
    memory: ArtifactReference | None = None
    mode: str | None = None
    hits: list[SearchMemoryHit]


@dataclass(frozen=True, slots=True)
class ResolvedConfig:
    """Effective connection configuration for one operation."""

    base_url: str
    scope_id: str
    token: str | None = field(repr=False)
    timeout: float
    max_bytes: int


def resolve_config(
    scope: PowerContextScope | None = None,
    *,
    settings: PowerContextLangGraphSettings | None = None,
) -> ResolvedConfig:
    """Overlay an optional run scope onto environment settings."""

    resolved_settings = settings or PowerContextLangGraphSettings()
    scope = scope or PowerContextScope()
    token = (
        scope.token
        if scope.token is not None
        else _secret_value(resolved_settings.token)
    )
    timeout = scope.timeout if scope.timeout is not None else resolved_settings.timeout
    if timeout <= 0:
        raise ValueError("PowerContext timeout must be positive")
    if token is not None and (
        not token or any(character.isspace() for character in token)
    ):
        raise ValueError("PowerContext token must be a non-empty bearer token")
    return ResolvedConfig(
        base_url=_normalize_http_base_url(scope.base_url or resolved_settings.base_url),
        scope_id=resolve_scope_id(scope.scope_id or resolved_settings.scope_id),
        token=token,
        timeout=timeout,
        max_bytes=resolved_settings.max_bytes,
    )


def _secret_value(secret: SecretStr | None) -> str | None:
    return secret.get_secret_value() if secret is not None else None


class PowerContextClient:
    """The narrow three-operation client required by this integration."""

    def __init__(
        self,
        base_url: str,
        *,
        token: str | None = None,
        timeout: float = 10.0,
        http_client: httpx.AsyncClient | None = None,
        trust_transport_security: bool = False,
    ) -> None:
        transport_trusted = http_client is not None and trust_transport_security
        self._base_url = _http_base_url(base_url, trust_transport_security=transport_trusted)
        if timeout <= 0:
            raise ValueError("PowerContext timeout must be positive")
        if token is not None and (
            not token or any(character.isspace() for character in token)
        ):
            raise ValueError("PowerContext token must be a non-empty bearer token")
        self._timeout = timeout
        self._token = token
        self._client = http_client or httpx.AsyncClient(follow_redirects=False)
        self._owns_client = http_client is None

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    async def prepare_context(self, request: PrepareContextRequest) -> PreparedContext:
        payload = await self._post("/v1/context/prepare", request)
        prepared = _validate(PreparedContext, payload)
        if prepared.content_bytes > request.max_bytes:
            raise InvalidResponseError(
                "prepared context exceeds its requested byte contract"
            )
        return prepared

    async def search_memory(self, request: SearchMemoryRequest) -> SearchMemoryResponse:
        return _validate(
            SearchMemoryResponse, await self._post("/v1/memory/search", request)
        )

    async def remember_memory(
        self, request: RememberMemoryRequest
    ) -> MemoryMutationResponse:
        return _validate(
            MemoryMutationResponse, await self._post("/v1/memory/remember", request)
        )

    async def _post(self, path: str, request: BaseModel) -> dict[str, Any]:
        headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": _USER_AGENT,
        }
        if self._token is not None:
            headers["Authorization"] = f"Bearer {self._token}"
        encoded = request.model_dump_json(exclude_none=False).encode("utf-8")
        response: httpx.Response | None = None
        try:
            async with asyncio.timeout(self._timeout):
                built = self._client.build_request(
                    "POST", self._base_url + path, headers=headers, content=encoded
                )
                response = await self._client.send(
                    built, stream=True, follow_redirects=False
                )
                if response.status_code != 200:
                    raise ServerResponseError(
                        response.status_code,
                        response.headers.get("X-PowerContext-Request-ID"),
                    )
                body = await _read_response(response)
        except ServerResponseError:
            raise
        except TimeoutError as error:
            raise TransportError("PowerContext Server request timed out") from error
        except httpx.HTTPError as error:
            raise TransportError("PowerContext Server is unavailable") from error
        finally:
            if response is not None:
                await response.aclose()
        try:
            value = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise InvalidResponseError(
                "PowerContext Server returned invalid JSON"
            ) from error
        if not isinstance(value, dict):
            raise InvalidResponseError("PowerContext Server response must be an object")
        return value


def open_client(config: ResolvedConfig) -> PowerContextClient:
    """Open a per-operation client, borrowing a shared pool when installed."""

    shared = _SHARED_HTTP_CLIENT.get()
    if shared is not None:
        client, trust_transport_security = shared
        return PowerContextClient(
            config.base_url,
            token=config.token,
            timeout=config.timeout,
            http_client=client,
            trust_transport_security=trust_transport_security,
        )
    return PowerContextClient(config.base_url, token=config.token, timeout=config.timeout)


@contextmanager
def shared_http_client(client: httpx.AsyncClient, *, trust_transport_security: bool = False) -> Iterator[None]:
    """Install a shared connection pool and its explicit transport-security vouch."""

    token = _SHARED_HTTP_CLIENT.set((client, trust_transport_security))
    try:
        yield
    finally:
        _SHARED_HTTP_CLIENT.reset(token)


async def _read_response(response: httpx.Response) -> bytes:
    declared = response.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_RESPONSE_BYTES:
                raise InvalidResponseError("PowerContext Server response exceeds 1 MiB")
        except ValueError as error:
            raise InvalidResponseError(
                "PowerContext Server returned an invalid Content-Length"
            ) from error
    chunks: list[bytes] = []
    total = 0
    async for chunk in response.aiter_bytes():
        total += len(chunk)
        if total > MAX_RESPONSE_BYTES:
            raise InvalidResponseError("PowerContext Server response exceeds 1 MiB")
        chunks.append(chunk)
    return b"".join(chunks)


def _validate(model: type[_WireModel], value: dict[str, Any]):
    try:
        return model.model_validate(value)
    except ValidationError as error:
        raise InvalidResponseError(
            "PowerContext Server response does not match the public contract"
        ) from error


def _normalize_http_base_url(value: str) -> str:
    parsed = urlsplit(value.rstrip("/"))
    if parsed.scheme not in {"http", "https"} or parsed.hostname is None:
        raise ValueError("PowerContext base URL must use HTTP or HTTPS")
    if (
        parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError(
            "PowerContext base URL must not contain credentials, query data, or a fragment"
        )
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip("/"), "", ""))


def _http_base_url(value: str, *, trust_transport_security: bool = False) -> str:
    normalized = _normalize_http_base_url(value)
    parsed = urlsplit(normalized)
    if parsed.scheme == "http" and not trust_transport_security and not _is_loopback_host(parsed.hostname or ""):
        raise ValueError("unencrypted PowerContext URLs must be loopback addresses")
    return normalized


def _is_loopback_host(host: str) -> bool:
    normalized = host.strip().lower()
    if normalized == "localhost":
        return True
    try:
        return ipaddress.ip_address(normalized).is_loopback
    except ValueError:
        return False


__all__ = [
    "ClientError",
    "InvalidResponseError",
    "MemoryMatchedBy",
    "PowerContextClient",
    "PrepareContextRequest",
    "PreparedContextStatus",
    "RememberMemoryRequest",
    "ResolvedConfig",
    "SearchMemoryRequest",
    "ServerResponseError",
    "TransportError",
    "open_client",
    "resolve_config",
    "shared_http_client",
]
