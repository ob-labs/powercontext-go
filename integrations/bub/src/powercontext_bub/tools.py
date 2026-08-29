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

"""PowerContext tools exposed to Bub agents."""

from __future__ import annotations

import json
import math
from typing import Any, TypedDict, cast

from bub import ensure_config, tool
from pydantic import BaseModel, Field

from .client import InvalidResponseError, PowerContextHTTPClient, require_list, validate_prepared_context
from .plugin import STATE_KEY, PowerContextSettings


class ToolSettings(TypedDict):
    base_url: str
    scope_id: str
    timeout: float
    token: str | None
    trust_transport_security: bool


class SearchInput(BaseModel):
    query: str = Field(description="Question or topic to recall from durable project memory.")
    limit: int = Field(5, ge=1, le=20, description="Maximum number of memory entries to return.")


class RememberInput(BaseModel):
    text: str = Field(description="Durable decision, preference, constraint, or procedure to remember.")
    kind: str = Field("agent-note", description="Stable category for the memory entry.")
    reason: str | None = Field(None, description="Why this information should remain available across sessions.")


@tool(context=True, name="powercontext.search", model=SearchInput)
async def search_memory(param: SearchInput, *, context: Any) -> str:
    """Search durable PowerContext memory."""

    settings = _settings(context)
    response = await _client(settings).post(
        "/v1/memory/search",
        {"scope_id": settings["scope_id"], "query": param.query, "limit": param.limit, "mode": "auto"},
        expected_status=200,
    )
    hits = require_list(response.get("hits"), "hits")
    if not hits:
        return "(no matching PowerContext memory)"

    projected = []
    for hit in hits:
        if not isinstance(hit, dict):
            raise InvalidResponseError("PowerContext memory hit is invalid")
        matched_by = hit.get("matched_by")
        score, text = hit.get("score"), hit.get("text")
        if (
            not isinstance(matched_by, list)
            or any(not isinstance(value, str) for value in matched_by)
            or not isinstance(score, int | float)
            or isinstance(score, bool)
            or not math.isfinite(float(score))
            or not 0 <= float(score) <= 1
            or not isinstance(text, str)
        ):
            raise InvalidResponseError("PowerContext memory hit is invalid")
        projected.append({"matched_by": matched_by, "score": score, "text": text})
    return json.dumps(
        projected,
        ensure_ascii=True,
        sort_keys=True,
    )


@tool(context=True, name="powercontext.remember", model=RememberInput)
async def remember_memory(param: RememberInput, *, context: Any) -> str:
    """Save one explicit durable memory for later agent sessions."""

    settings = _settings(context)
    response = await _client(settings).post(
        "/v1/memory/remember",
        {"scope_id": settings["scope_id"], "kind": param.kind, "text": param.text, "reason": param.reason},
        expected_status=200,
    )
    entry = response.get("entry")
    if entry is None:
        return "(PowerContext accepted the memory without an entry receipt)"
    if not isinstance(entry, dict) or not isinstance(entry.get("kind"), str) or not isinstance(entry.get("text"), str):
        raise InvalidResponseError("PowerContext memory entry receipt is invalid")
    return f"Remembered {entry['kind']}: {entry['text']}"


@tool(context=True, name="powercontext.context")
async def prepare_context(query: str, *, context: Any) -> str:
    """Prepare a bounded PowerContext payload for a new question."""

    settings = _settings(context)
    response = await _client(settings).post(
        "/v1/context/prepare",
        {"scope_id": settings["scope_id"], "query": query, "max_bytes": 8000},
        expected_status=200,
    )
    prepared = validate_prepared_context(response, max_bytes=8000)
    content = prepared["content"]
    return content if isinstance(content, str) else "(no relevant PowerContext context)"


def _settings(context: Any) -> ToolSettings:
    settings = context.state[STATE_KEY]
    configured = ensure_config(PowerContextSettings)
    token = None if configured.api_token is None else configured.api_token.get_secret_value()
    return cast(ToolSettings, {**settings, "token": token})


def _client(settings: ToolSettings) -> PowerContextHTTPClient:
    return PowerContextHTTPClient(
        settings["base_url"],
        token=settings["token"],
        timeout=settings["timeout"],
        trust_transport_security=settings["trust_transport_security"],
    )
