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

"""Pydantic AI capability chains against the real Go Server process."""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path
from typing import Any

from pydantic_ai import Agent, RunContext
from pydantic_ai.messages import ModelResponse, SystemPromptPart, TextPart, ToolCallPart, ToolReturnPart
from pydantic_ai.models.function import FunctionModel

from powercontext_pydantic_ai import PowerContext, PowerContextSettings
from powercontext_pydantic_ai.capability import CONTEXT_MARKER

_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(_ROOT / "test" / "retainedhost"))
from go_server import running_go_server


def test_pydantic_ai_capture_checkpoint_recall_and_search_chain(tmp_path: Path) -> None:
    scope_id = "pydantic-ai-chain"
    recalled_contexts: list[str] = []
    search_results: list[dict[str, Any]] = []

    async def produce_evidence(_: RunContext[object]) -> dict[str, str]:
        return {"finding": "checkpoint-evidence is available after the tool completes"}

    async def respond(messages: list[Any], _: Any) -> ModelResponse:
        latest_returns = [part for part in messages[-1].parts if isinstance(part, ToolReturnPart)]
        if not latest_returns:
            return ModelResponse(parts=[ToolCallPart("produce_evidence", {}, "produce-1")])
        latest = latest_returns[-1]
        if latest.tool_name == "produce_evidence":
            contexts = [part.content for message in messages for part in message.parts if isinstance(part, SystemPromptPart) and CONTEXT_MARKER in part.content]
            if contexts:
                recalled_contexts.append(contexts[-1])
            return ModelResponse(parts=[ToolCallPart("powercontext_search", {"query": "checkpoint-evidence", "mode": "fts"}, "search-1")])
        assert latest.tool_name == "powercontext_search"
        assert isinstance(latest.content, dict)
        search_results.append(latest.content)
        return ModelResponse(parts=[TextPart("capture, checkpoint, recall, and search completed")])

    with running_go_server(tmp_path) as server:
        async def scenario() -> str:
            settings = PowerContextSettings(base_url=server.base_url, capture_events=True, capture_checkpoint_every=3)
            agent: Agent[object, str] = Agent(FunctionModel(respond), output_type=str, deps_type=object, tools=[produce_evidence], capabilities=[PowerContext[object](settings=settings, scope_id=scope_id)])
            return (await agent.run("Find checkpoint-evidence with a tool, then recall and search it.")).output
        assert asyncio.run(scenario()) == "capture, checkpoint, recall, and search completed"
    assert recalled_contexts and "checkpoint-evidence" in recalled_contexts[-1]
    assert search_results and search_results[0]["hits"]


def test_pydantic_ai_final_flush_catches_up_across_more_than_ten_source_windows(tmp_path: Path) -> None:
    scope_id = "pydantic-ai-deep-backlog"
    evidence = "deep-backlog-evidence is immediately recallable"
    recalled_contexts: list[str] = []

    async def produce_evidence(_: RunContext[object]) -> dict[str, str]:
        return {"finding": evidence}

    async def capture_respond(messages: list[Any], _: Any) -> ModelResponse:
        tool_returns = [part for message in messages for part in message.parts if isinstance(part, ToolReturnPart) and part.tool_name == "produce_evidence"]
        if not tool_returns:
            return ModelResponse(parts=[ToolCallPart("produce_evidence", {}, "produce-backlog-1")])
        return ModelResponse(parts=[TextPart("evidence captured")])

    async def recall_respond(messages: list[Any], _: Any) -> ModelResponse:
        contexts = [part.content for message in messages for part in message.parts if isinstance(part, SystemPromptPart) and CONTEXT_MARKER in part.content]
        assert contexts and evidence in contexts[-1]
        recalled_contexts.append(contexts[-1])
        return ModelResponse(parts=[TextPart("read-your-write preserved")])

    with running_go_server(tmp_path, source_window_limit=1) as server:
        async def scenario() -> str:
            settings = PowerContextSettings(base_url=server.base_url, capture_events=True, capture_checkpoint_every=100, timeout=30)
            capture_agent: Agent[object, str] = Agent(FunctionModel(capture_respond), output_type=str, deps_type=object, tools=[produce_evidence], capabilities=[PowerContext[object](settings=settings, scope_id=scope_id)])
            assert (await capture_agent.run("Capture deep-backlog-evidence with the tool.")).output == "evidence captured"
            recall_agent = Agent(FunctionModel(recall_respond), capabilities=[PowerContext(settings=PowerContextSettings(base_url=server.base_url), scope_id=scope_id)])
            return (await recall_agent.run("Recall deep-backlog-evidence now.")).output
        assert asyncio.run(scenario()) == "read-your-write preserved"
    assert recalled_contexts
