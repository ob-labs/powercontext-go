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

"""Real Go Server behavior tests for the LangChain middleware."""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path
from typing import Any

from langchain.agents import create_agent
from langchain.agents.structured_output import ToolStrategy
from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage, HumanMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langchain_core.runnables import Runnable
from langchain_core.tools import BaseTool
from pydantic import BaseModel, Field
from powercontext.client import PowerContextClient
from powercontext.http import (
    ApproveArtifactCandidateRequest,
    CaptureContentSourceRequest,
    ExperienceProposal,
    FlushMemoryRequest,
    ListMemoryEntriesRequest,
    ProposeExperienceRequest,
)

from powercontext_langchain import PowerContextMiddleware, PowerContextScope

_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(_ROOT / "test" / "retainedhost"))
from go_server import running_go_server

SCOPE = "project:langchain-middleware-test"
MEMORY_TEXT = "Run database migrations before deploying the application."
FINAL_ANSWER = "Apply the migrations first, then deploy the application."


class _RecordingModel(BaseChatModel):
    inputs: list[list[BaseMessage]] = Field(default_factory=list)

    def _generate(self, messages: list[BaseMessage], stop: Any = None, run_manager: Any = None, **_: Any) -> ChatResult:
        del stop, run_manager
        self.inputs.append(list(messages))
        return ChatResult(generations=[ChatGeneration(message=AIMessage(content=FINAL_ANSWER))])

    @property
    def _llm_type(self) -> str:
        return "recording"


class _DeploymentPlan(BaseModel):
    order: str


class _StructuredRecordingModel(_RecordingModel):
    def _generate(self, messages: list[BaseMessage], stop: Any = None, run_manager: Any = None, **_: Any) -> ChatResult:
        del stop, run_manager
        self.inputs.append(list(messages))
        return ChatResult(generations=[ChatGeneration(message=AIMessage(content="", tool_calls=[{"name": "_DeploymentPlan", "args": {"order": "migrate-first"}, "id": "plan", "type": "tool_call"}]))])

    def bind_tools(self, tools: list[dict[str, Any] | type | BaseTool], **_: Any) -> Runnable[Any, AIMessage]:
        del tools
        return self


def _system_texts(messages: list[BaseMessage]) -> list[str]:
    return [message.text for message in messages if message.type == "system"]


def _agent(model: BaseChatModel, *, auto_capture: bool = False, structured: bool = False):
    return create_agent(model, tools=[], middleware=[PowerContextMiddleware(auto_capture=auto_capture)], context_schema=PowerContextScope, response_format=ToolStrategy(_DeploymentPlan) if structured else None)


def test_middleware_injects_recall_without_persisting_it(tmp_path: Path) -> None:
    model = _RecordingModel()
    with running_go_server(tmp_path) as server:
        async def scenario() -> None:
            async with PowerContextClient(server.base_url) as client:
                await client.capture_content_source(
                    CaptureContentSourceRequest(scope_id=SCOPE, source_id="migration-memory", content=MEMORY_TEXT)
                )
                await client.flush_memory(FlushMemoryRequest(scope_id=SCOPE))
            result = await _agent(model).ainvoke({"messages": [HumanMessage(content="migrations")]}, context=PowerContextScope(scope_id=SCOPE, base_url=server.base_url))
            assert MEMORY_TEXT in _system_texts(model.inputs[-1])[0]
            assert _system_texts(result["messages"]) == []
        asyncio.run(scenario())


def test_middleware_injects_only_approved_experience(tmp_path: Path) -> None:
    model = _RecordingModel()
    with running_go_server(tmp_path) as server:
        async def scenario() -> None:
            async with PowerContextClient(server.base_url) as client:
                source = await client.capture_content_source(
                    CaptureContentSourceRequest(
                        scope_id=SCOPE,
                        source_id="approved-experience-evidence",
                        content="The coralblueprint migration-first deployment completed without schema errors.",
                    )
                )
                candidate = await client.propose_experience(
                    ProposeExperienceRequest(
                        scope_id=SCOPE,
                        proposal=ExperienceProposal(
                            situation="A coralblueprint deployment includes schema changes.",
                            action="Apply database migrations before deploying the application.",
                            outcome="The application starts against the expected schema.",
                            lesson="Migrate the database before rolling out application code.",
                        ),
                        source_refs=[source.source],
                        artifact_refs=[],
                    )
                )
            scope = PowerContextScope(scope_id=SCOPE, base_url=server.base_url)
            pending = await _agent(model).ainvoke({"messages": [HumanMessage(content="coralblueprint")]}, context=scope)
            assert _system_texts(model.inputs[-1]) == []
            assert _system_texts(pending["messages"]) == []
            async with PowerContextClient(server.base_url) as client:
                await client.approve_artifact_candidate(
                    ApproveArtifactCandidateRequest(scope_id=SCOPE, candidate_id=candidate.candidate_id, expected_version=candidate.version)
                )
            approved = await _agent(model).ainvoke({"messages": [HumanMessage(content="coralblueprint")]}, context=scope)
            assert "coralblueprint" in _system_texts(model.inputs[-1])[0]
            assert _system_texts(approved["messages"]) == []
        asyncio.run(scenario())


def test_middleware_does_not_capture_completed_turn_by_default(tmp_path: Path) -> None:
    with running_go_server(tmp_path) as server:
        async def scenario() -> None:
            await _agent(_RecordingModel()).ainvoke({"messages": [HumanMessage(content="no capture")]}, context=PowerContextScope(scope_id=SCOPE, base_url=server.base_url))
            async with PowerContextClient(server.base_url) as client:
                await client.flush_memory(FlushMemoryRequest(scope_id=SCOPE))
                assert (await client.list_memory_entries(ListMemoryEntriesRequest(scope_id=SCOPE))).entries == []
        asyncio.run(scenario())


def test_middleware_captures_completed_turn_when_enabled(tmp_path: Path) -> None:
    user_text = "What is the safe deployment order?"
    with running_go_server(tmp_path) as server:
        async def scenario() -> None:
            await _agent(_RecordingModel(), auto_capture=True).ainvoke({"messages": [HumanMessage(content=user_text)]}, context=PowerContextScope(scope_id=SCOPE, base_url=server.base_url))
            async with PowerContextClient(server.base_url) as client:
                await client.flush_memory(FlushMemoryRequest(scope_id=SCOPE))
                entries = (await client.list_memory_entries(ListMemoryEntriesRequest(scope_id=SCOPE))).entries
            assert len(entries) == 1
            assert entries[0].text == f"User:\n{user_text}\n\nAssistant:\n{FINAL_ANSWER}"
        asyncio.run(scenario())


def test_middleware_captures_tool_strategy_structured_response(tmp_path: Path) -> None:
    with running_go_server(tmp_path) as server:
        async def scenario() -> None:
            await _agent(_StructuredRecordingModel(), auto_capture=True, structured=True).ainvoke({"messages": [HumanMessage(content="Return the deployment plan.")]}, context=PowerContextScope(scope_id=SCOPE, base_url=server.base_url))
            async with PowerContextClient(server.base_url) as client:
                await client.flush_memory(FlushMemoryRequest(scope_id=SCOPE))
                entries = (await client.list_memory_entries(ListMemoryEntriesRequest(scope_id=SCOPE))).entries
            assert len(entries) == 1
            assert '"order":"migrate-first"' in entries[0].text
        asyncio.run(scenario())


def test_async_agent_reaches_end_when_server_unreachable() -> None:
    model = _RecordingModel()
    result = asyncio.run(_agent(model).ainvoke({"messages": [HumanMessage(content="Continue without memory.")]}, context=PowerContextScope(scope_id=SCOPE, base_url="http://127.0.0.1:9", timeout=0.2)))
    assert result["messages"][-1].text == FINAL_ANSWER
    assert _system_texts(model.inputs[-1]) == []


def test_sync_agent_reaches_end_when_server_unreachable() -> None:
    model = _RecordingModel()
    result = _agent(model).invoke({"messages": [HumanMessage(content="Continue without memory.")]}, context=PowerContextScope(scope_id=SCOPE, base_url="http://127.0.0.1:9", timeout=0.2))
    assert result["messages"][-1].text == FINAL_ANSWER
    assert _system_texts(model.inputs[-1]) == []
