# Copyright (c) 2026 OceanBase.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

"""Public middleware behavior tests with an in-process PowerContext fake."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from types import SimpleNamespace
from typing import Any

from langchain_core.messages import AIMessage, HumanMessage

from powercontext.http import PreparedContextStatus
from powercontext_langchain import PowerContextMiddleware, PowerContextScope
from powercontext_langchain.client import ResolvedConfig


@dataclass
class _Request:
    messages: list[Any]
    system_message: Any = None
    runtime: Any = None

    def override(self, **changes: Any) -> "_Request":
        values = {"messages": self.messages, "system_message": self.system_message, "runtime": self.runtime}
        values.update(changes)
        return _Request(**values)


class _FakeClient:
    def __init__(self, *, content: str | None = "remembered", fail: Exception | None = None) -> None:
        self.content = content
        self.fail = fail
        self.prepared: list[Any] = []
        self.captured: list[Any] = []

    async def __aenter__(self) -> "_FakeClient":
        return self

    async def __aexit__(self, *_: Any) -> None:
        return None

    async def prepare_context(self, request: Any) -> Any:
        if self.fail:
            raise self.fail
        self.prepared.append(request)
        status = PreparedContextStatus.EMPTY if self.content is None else SimpleNamespace(value="ready")
        return SimpleNamespace(status=status, content=self.content or "")

    async def capture_content_source(self, request: Any) -> None:
        if self.fail:
            raise self.fail
        self.captured.append(request)


def _config() -> ResolvedConfig:
    return ResolvedConfig(
        base_url="http://127.0.0.1:8000",
        scope_id="project:test",
        token=None,
        timeout=1,
        max_bytes=8000,
    )


def test_async_model_call_injects_recall_without_mutating_messages(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    fake = _FakeClient()
    monkeypatch.setattr("powercontext_langchain.middleware.resolve_config", lambda _scope: _config())
    monkeypatch.setattr("powercontext_langchain.middleware.open_client", lambda _config: fake)
    request = _Request([HumanMessage(content="question")], runtime=SimpleNamespace(context=PowerContextScope()))
    seen: list[_Request] = []

    async def handler(value: _Request) -> object:
        seen.append(value)
        return object()

    asyncio.run(PowerContextMiddleware().awrap_model_call(request, handler))

    assert len(fake.prepared) == 1
    assert len(seen) == 1
    assert "remembered" in str(seen[0].system_message.content)
    assert request.messages == [request.messages[0]]


def test_async_model_call_fails_open_and_preserves_result(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    fake = _FakeClient(fail=OSError("offline"))
    monkeypatch.setattr("powercontext_langchain.middleware.resolve_config", lambda _scope: _config())
    monkeypatch.setattr("powercontext_langchain.middleware.open_client", lambda _config: fake)
    request = _Request([HumanMessage(content="question")])
    result = object()

    async def handler(value: _Request) -> object:
        assert value.system_message is None
        return result

    assert asyncio.run(PowerContextMiddleware().awrap_model_call(request, handler)) is result


def test_after_agent_captures_completed_turn(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    fake = _FakeClient()
    monkeypatch.setattr("powercontext_langchain.middleware.resolve_config", lambda _scope: _config())
    monkeypatch.setattr("powercontext_langchain.middleware.open_client", lambda _config: fake)
    state = {"messages": [HumanMessage(content="question"), AIMessage(content="answer")]}

    asyncio.run(PowerContextMiddleware(auto_capture=True).aafter_agent(state, SimpleNamespace(context=None)))

    assert len(fake.captured) == 1
    assert "question" in fake.captured[0].content
    assert "answer" in fake.captured[0].content
    assert fake.captured[0].metadata["integration"] == "langchain"


def test_missing_runtime_context_is_fail_open(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    fake = _FakeClient()
    monkeypatch.setattr("powercontext_langchain.middleware.resolve_config", lambda _scope: _config())
    monkeypatch.setattr("powercontext_langchain.middleware.open_client", lambda _config: fake)
    request = _Request([HumanMessage(content="question")], runtime=object())

    async def handler(value: _Request) -> object:
        return value

    result = asyncio.run(PowerContextMiddleware().awrap_model_call(request, handler))
    assert result is not None


def test_empty_recall_does_not_add_system_message(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    fake = _FakeClient(content=None)
    monkeypatch.setattr("powercontext_langchain.middleware.resolve_config", lambda _scope: _config())
    monkeypatch.setattr("powercontext_langchain.middleware.open_client", lambda _config: fake)
    request = _Request([HumanMessage(content="question")])

    async def handler(value: _Request) -> _Request:
        return value

    result = asyncio.run(PowerContextMiddleware().awrap_model_call(request, handler))
    assert result.system_message is None
