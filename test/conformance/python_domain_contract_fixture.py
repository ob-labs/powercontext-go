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

"""Freeze public domain constants and HTTP error mappings from Python v0.0.2."""

from __future__ import annotations

import argparse
import json
from importlib import import_module
from pathlib import Path

from powercontext.artifacts import ArtifactRef
from powercontext.builtin.artifacts.memory import (
    EmbeddingProfile,
    MemoryChange,
    MemoryContent,
    MemoryManifest,
    MemoryManifestEntry,
)
from powercontext.builtin.artifacts.memory.canonical import (
    embedding_content_hash,
    entry_content_bytes,
    entry_content_hash,
    memory_content_bytes,
    memory_content_hash,
    normalize_embedding,
    normalize_kind,
    normalize_reason,
    normalize_text,
)
from powercontext.builtin.artifacts.handoff.errors import (
    HandoffEvidenceUnavailableError,
    HandoffGenerationUnavailableError,
    HandoffScopeMismatchError,
    InvalidHandoffGenerationError,
    InvalidHandoffReferenceError,
)
from powercontext.builtin.artifacts.memory.errors import (
    CapabilityNotSupportedError,
    InvalidMemoryCandidateError,
    InvalidMemoryCitationError,
    InvalidMemoryEvidenceError,
    MemoryEntryInactiveError,
    MemoryEntryNotFoundError,
)
from powercontext.builtin.artifacts.skill.external import (
    ExternalSkillNotFoundError,
    ExternalSkillRegistryUnavailableError,
    ExternalSkillSnapshotUnavailableError,
)
from powercontext.builtin.handoff_report.errors import (
    HandoffReportBusyError,
    HandoffReportCatalogArgumentError,
    HandoffReportEvidenceCheckUnavailableError,
    HandoffReportInconsistentError,
    HandoffReportTooLargeError,
    ProjectConflictError,
    ProjectNotFoundError,
    ScopeAlreadyGroupedError,
    WorkspaceBindingConflictError,
    WorkspaceBindingNotFoundError,
    WorkstreamConflictError,
    WorkstreamNotFoundError,
)
from powercontext.builtin.handoff_report.repository import (
    ActivityEventConflictError,
    InvalidActivityEventError,
    InvalidActivityRepositoryArgumentError,
)
from powercontext.builtin.inference.errors import InferenceTimeoutError, InferenceUnavailableError
from powercontext.builtin.review.errors import (
    ArtifactTargetConflictError,
    CandidateConflictError,
    CandidateNotFoundError,
    CandidateTerminalError,
    InvalidCandidateError,
)
from powercontext.builtin.review.generation import GenerationCapabilityUnavailableError
from powercontext.builtin.runtime.errors import InvalidRuntimeRequestError
from powercontext.errors import (
    ArtifactNotFoundError,
    RevisionConflictError,
    SourceConflictError,
)
from powercontext.server.app import _RuntimeNotReadyError, _map_error

ORACLE_COMMIT = "3a6cb0151670eaff7dc0293466edd673124e80da"

CONSTANTS: dict[str, tuple[str, ...]] = {
    "powercontext.limits": (
        "MAX_SCOPE_ID_LENGTH",
        "MAX_SOURCE_ID_LENGTH",
        "MAX_SOURCE_TYPE_LENGTH",
        "MAX_ARTIFACT_FAMILY_LENGTH",
        "MAX_ARTIFACT_ID_LENGTH",
        "MAX_BINDING_NAME_LENGTH",
        "MAX_EXTERNAL_SKILL_NAME_LENGTH",
        "MAX_EXTERNAL_SKILL_DESCRIPTION_LENGTH",
        "MAX_EXTERNAL_SKILL_HOST_ID_LENGTH",
        "MAX_EXTERNAL_SKILL_LOCATOR_LENGTH",
    ),
    "powercontext.builtin.artifacts.generation": (
        "MAX_GENERATION_EVIDENCE",
        "MAX_GENERATION_EVIDENCE_CHARS",
    ),
    "powercontext.builtin.sources.content": ("CONTENT_SOURCE_NAME",),
    "powercontext.builtin.sources.external_skill": ("EXTERNAL_SKILL_SNAPSHOT_SOURCE_NAME",),
    "powercontext.builtin.artifacts.experience.models": ("MAX_EXPERIENCE_FIELD_LENGTH",),
    "powercontext.builtin.artifacts.experience.incubation": (
        "TASK_OUTCOME_SOURCE_KIND",
        "EXPERIENCE_INCUBATION_CURSOR_NAME",
        "EXPERIENCE_INCUBATION_WINDOW_LIMIT",
        "MAX_EXPERIENCE_INCUBATION_SOURCES",
        "MAX_EXPERIENCE_INCUBATION_SOURCE_CHARS",
        "MAX_EXPERIENCE_CANDIDATE_EVIDENCE",
        "EXPERIENCE_INCUBATION_REASON",
    ),
    "powercontext.builtin.artifacts.skill.models": (
        "MAX_SKILL_NAME_LENGTH",
        "MAX_SKILL_DESCRIPTION_LENGTH",
        "MAX_SKILL_INSTRUCTIONS_LENGTH",
        "MAX_SKILL_VALIDATION_ITEMS",
        "MAX_SKILL_VALIDATION_ITEM_LENGTH",
    ),
    "powercontext.builtin.artifacts.skill.external": (
        "MAX_EXTERNAL_SKILL_FILES",
        "MAX_EXTERNAL_SKILL_PACKAGE_BYTES",
        "MAX_EXTERNAL_SKILL_MANIFEST_BYTES",
    ),
    "powercontext.builtin.artifacts.handoff.models": (
        "DEFAULT_HANDOFF_MAX_BYTES",
        "MAX_HANDOFF_BYTES",
        "MIN_HANDOFF_MAX_BYTES",
        "MAX_HANDOFF_CITATIONS",
        "MAX_HANDOFF_OMISSIONS",
        "MAX_HANDOFF_STATE_STATEMENTS",
        "MAX_HANDOFF_TEXT_LENGTH",
    ),
    "powercontext.builtin.review.models": (
        "MAX_CANDIDATE_EVIDENCE",
        "MAX_CANDIDATE_PAGE_SIZE",
        "DEFAULT_CANDIDATE_PAGE_SIZE",
        "MAX_CANDIDATE_REASON_LENGTH",
    ),
    "powercontext.builtin.handoff_report.models": (
        "MAX_REPORT_ID_LENGTH",
        "MAX_PROJECT_KEY_LENGTH",
        "MAX_WORKSTREAM_KEY_LENGTH",
        "MAX_REPORT_TITLE_LENGTH",
        "MAX_REPORT_DESCRIPTION_LENGTH",
        "MAX_REPORT_LABEL_LENGTH",
        "MAX_REPORT_PROVIDER_LENGTH",
        "MAX_REPORT_AGENT_LABEL_LENGTH",
        "MAX_REPORT_SOURCE_SUMMARY_LENGTH",
        "MAX_REPORT_EXTERNAL_ID_LENGTH",
        "MAX_REPORT_URL_LENGTH",
        "MAX_REPORT_REPOSITORY_ID_LENGTH",
        "MAX_REPORT_NORMALIZED_REMOTE_LENGTH",
        "MAX_REPORT_SUBPATH_LENGTH",
        "MAX_WORKSPACE_INSTANCE_ID_LENGTH",
        "MAX_REPORT_EXTERNAL_REFS",
        "MAX_REPORT_LABELS",
        "MAX_REPORT_EVIDENCE_REFS",
    ),
    "powercontext.builtin.handoff_report.report": (
        "MAX_REPORT_WORKSTREAMS",
        "MAX_REPORT_ACTIVITIES",
    ),
    "powercontext.builtin.handoff_report.catalog_store": (
        "DEFAULT_CATALOG_PAGE_SIZE",
        "MAX_CATALOG_PAGE_SIZE",
    ),
    "powercontext.builtin.handoff_report.selection": (
        "DEFAULT_HANDOFF_SELECTION_ATTEMPTS",
        "MAX_HANDOFF_SELECTION_ATTEMPTS",
    ),
    "powercontext.builtin.runtime.models": ("PREPARED_CONTEXT_SCHEMA",),
    "powercontext.builtin.runtime.readiness": (
        "READINESS_PROBE_TIMEOUT_SECONDS",
        "READINESS_PROBE_CACHE_SECONDS",
        "READINESS_PROBE_TRANSIENT_CACHE_SECONDS",
    ),
    "powercontext.builtin.runtime.scheduler": (
        "SOURCE_WINDOW_JOB_ID",
        "EXPERIENCE_INCUBATION_JOB_ID",
        "SCHEDULER_TABLE",
    ),
    "powercontext.builtin.triggers.source_window": ("SOURCE_WINDOW_TRIGGER_NAME",),
    "powercontext.builtin.triggers.handoff": ("HANDOFF_BOUNDARY_TRIGGER_NAME",),
    "powercontext.server.app": (
        "REQUEST_ID_HEADER",
        "REPORT_SELECTION_DIGEST_HEADER",
        "REPORT_DIGEST_HEADER",
        "MAX_HANDOFF_REPORT_BYTES",
    ),
    "powercontext.server.mcp": ("MCP_PATH", "MCP_SERVER_NAME"),
}


def constants() -> dict[str, object]:
    values: dict[str, object] = {}
    for module_name, names in CONSTANTS.items():
        module = import_module(module_name)
        for name in names:
            value = getattr(module, name)
            assert isinstance(value, (str, int, float)) and not isinstance(value, bool)
            values[f"{module_name}.{name}"] = value
    return values


def error_cases() -> tuple[tuple[str, Exception], ...]:
    target = ArtifactRef(family="experience", artifact_id="exp-1", revision=1)
    current = ArtifactRef(family="experience", artifact_id="exp-1", revision=2)
    return (
        ("runtime_not_ready", _RuntimeNotReadyError()),
        ("external_registry_unavailable", ExternalSkillRegistryUnavailableError()),
        ("external_not_found", ExternalSkillNotFoundError("skill-1")),
        ("external_snapshot_unavailable", ExternalSkillSnapshotUnavailableError("skill-1")),
        ("generation_unavailable", GenerationCapabilityUnavailableError("skill")),
        ("candidate_not_found", CandidateNotFoundError("candidate-1")),
        ("candidate_conflict", CandidateConflictError("candidate-1", 2, 4)),
        ("artifact_target_conflict", ArtifactTargetConflictError(target, current)),
        ("candidate_terminal", CandidateTerminalError("candidate-1", "approved")),
        ("invalid_candidate", InvalidCandidateError("proposal", "invalid")),
        ("project_not_found", ProjectNotFoundError("project-1")),
        ("workstream_not_found", WorkstreamNotFoundError("scope-1")),
        ("workspace_not_found", WorkspaceBindingNotFoundError("workspace-1")),
        ("project_conflict", ProjectConflictError("project-1", 2, 4)),
        ("workstream_conflict", WorkstreamConflictError("scope-1", 2, 4)),
        ("scope_already_grouped", ScopeAlreadyGroupedError("scope-1", "project-1")),
        ("workspace_conflict", WorkspaceBindingConflictError("workspace-1", 2, 4)),
        ("activity_conflict", ActivityEventConflictError("coding_session", "session-1")),
        ("report_busy", HandoffReportBusyError(3)),
        (
            "report_too_large",
            HandoffReportTooLargeError(
                selected_workstreams=2,
                selected_activities=3,
                estimated_bytes=10_485_761,
            ),
        ),
        ("report_inconsistent", HandoffReportInconsistentError("scope-1")),
        ("report_invalid_catalog", HandoffReportCatalogArgumentError("period", "invalid")),
        ("report_invalid_activity", InvalidActivityEventError("source", "invalid")),
        ("report_invalid_repository", InvalidActivityRepositoryArgumentError("limit", "invalid")),
        ("report_unavailable", HandoffReportEvidenceCheckUnavailableError()),
        ("artifact_not_found", ArtifactNotFoundError(target)),
        ("memory_not_found", MemoryEntryNotFoundError("entry-1")),
        ("source_conflict", SourceConflictError("identity", "source-1")),
        ("revision_conflict", RevisionConflictError(target, current)),
        ("memory_inactive", MemoryEntryInactiveError("entry-1")),
        ("capability_not_supported", CapabilityNotSupportedError("vector")),
        ("invalid_memory_candidate", InvalidMemoryCandidateError("canonical")),
        ("invalid_memory_evidence", InvalidMemoryEvidenceError("source-outside")),
        ("invalid_memory_citation", InvalidMemoryCitationError("hash-mismatch")),
        ("handoff_scope_mismatch", HandoffScopeMismatchError("scope-1", "scope-2")),
        ("invalid_handoff_reference", InvalidHandoffReferenceError(target)),
        ("invalid_runtime_request", InvalidRuntimeRequestError("since-revision")),
        ("inference_timeout", InferenceTimeoutError("generation", 1)),
        ("inference_unavailable", InferenceUnavailableError("generation")),
        ("handoff_evidence_not_found", HandoffEvidenceUnavailableError(target)),
        ("handoff_generation_unavailable", HandoffGenerationUnavailableError()),
        ("invalid_handoff_generation", InvalidHandoffGenerationError("budget")),
        ("unknown", RuntimeError("secret backend detail")),
    )


def error_mappings() -> dict[str, object]:
    result: dict[str, object] = {}
    for name, error in error_cases():
        status, code, message, details = _map_error(error)
        result[name] = {
            "python_type": f"{type(error).__module__}.{type(error).__qualname__}",
            "status": status,
            "code": code,
            "message": message,
            "details": details,
        }
    return result


def memory_canonical_contract() -> dict[str, object]:
    """Freeze byte-for-byte Memory authority values from the Python runtime."""

    separators = "\u001c\u001d\u001e\u001f"
    source_refs = (
        {"source_type": "content", "source_id": "b"},
        {"source_type": "content", "source_id": "a"},
        {"source_type": "content", "source_id": "a"},
    )
    artifact_refs = ({"family": "experience", "artifact_id": "z", "revision": 2},)
    kind = f"{separators} fact {separators}"
    text = f"{separators}Cafe\N{COMBINING ACUTE ACCENT}  {separators}"
    entry_bytes = entry_content_bytes(
        kind=kind,
        text=text,
        source_refs=source_refs,
        artifact_refs=artifact_refs,
    )
    entry_hash = entry_content_hash(
        kind=kind,
        text=text,
        source_refs=source_refs,
        artifact_refs=artifact_refs,
    )
    manifest_entry = MemoryManifestEntry(
        entry_id="entry-a",
        entry_version_id="version-a1",
        entry_content_hash=entry_hash,
        state="active",
    )
    content = MemoryContent(
        manifest=MemoryManifest(entries=(manifest_entry,)),
        changes=(
            MemoryChange(
                op="add",
                entry_id="entry-a",
                from_entry_version_id=None,
                to_entry_version_id="version-a1",
                reason=f"{separators} introduced {separators}",
            ),
        ),
    )
    profile = EmbeddingProfile(
        profile_id=" profile-a ",
        model=" model-a ",
        dimension=3,
        distance="l2",
        normalization="unit",
    )
    return {
        "normalization": {
            "text": normalize_text(f"{separators} durable {separators}"),
            "kind": normalize_kind(f"{separators} integration-kind {separators}"),
            "reason": normalize_reason(f"{separators} user requested {separators}"),
        },
        "entry": {"bytes": entry_bytes.decode("utf-8"), "hash": entry_hash},
        "memory": {
            "bytes": memory_content_bytes(content).decode("utf-8"),
            "hash": memory_content_hash(content),
        },
        "embedding": {
            "hash": embedding_content_hash(
                profile_id=profile.profile_id,
                model=profile.model,
                dimension=profile.dimension,
                distance=profile.distance,
                normalization=profile.normalization,
                entry_content_hash=entry_hash,
            ),
            "overflow_stable_unit": normalize_embedding((1e308, 1e308), dimension=2),
        },
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("output", type=Path)
    parser.add_argument("--baseline", default="v0.0.2")
    parser.add_argument("--oracle-commit", default=ORACLE_COMMIT)
    arguments = parser.parse_args()
    fixture = {
        "schema": f"powercontext.python-{arguments.baseline}.domain-contract.v1",
        "oracle_commit": arguments.oracle_commit,
        "constants": constants(),
        "error_mappings": error_mappings(),
        "memory_canonical": memory_canonical_contract(),
    }
    arguments.output.write_text(
        json.dumps(fixture, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )


if __name__ == "__main__":
    main()
