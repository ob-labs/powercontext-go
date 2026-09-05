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

"""Compare the frozen Python Server and the Go Server through public HTTP."""

from __future__ import annotations

import argparse
import contextlib
import copy
import difflib
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
from collections.abc import Iterator, Mapping
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import ProxyHandler, Request, build_opener

_DYNAMIC_JSON_FIELDS = frozenset(
    {"as_of", "generated_at", "report_digest", "request_id"}
)
_GO_CAPABILITIES_EXTENSIONS = frozenset(
    {"supported_databases", "supported_external_agents"}
)
_SECRET_ENV_MARKERS = ("API_KEY", "AUTH_TOKEN", "PASSWORD", "SECRET", "TOKEN")
_LOOPBACK_OPENER = build_opener(ProxyHandler({}))
_POST_PATHS = (
    "/v1/sources/content",
    "/v1/context/prepare",
    "/v1/work/contracts/create",
    "/v1/work/handoffs/prepare-current",
    "/v1/work/handoffs/acknowledge",
    "/v1/work/outcomes/record",
    "/v1/handoff/activate",
    "/v1/handoff/prepare",
    "/v1/handoff/finalize",
    "/v1/handoff/commit",
    "/v1/handoff/continue",
    "/v1/memory/flush",
    "/v1/memory/remember",
    "/v1/memory/search",
    "/v1/memory/entries/list",
    "/v1/memory/entries/get",
    "/v1/memory/entries/revise",
    "/v1/memory/entries/retire",
    "/v1/memory/changes",
    "/v1/experience/propose",
    "/v1/experience/generate",
    "/v1/experience/get",
    "/v1/skill/propose",
    "/v1/skill/generate",
    "/v1/skill/get",
    "/v1/external-skills/scan",
    "/v1/external-skills/list",
    "/v1/external-skills/resolve",
    "/v1/external-skills/import",
    "/v1/artifact-candidates/list",
    "/v1/artifact-candidates/get",
    "/v1/artifact-candidates/approve",
    "/v1/artifact-candidates/reject",
    "/v1/artifact-candidates/revise",
    "/v1/handoff-reports/projects/create",
    "/v1/handoff-reports/projects/list",
    "/v1/handoff-reports/scopes/list-known",
    "/v1/handoff-reports/projects/get",
    "/v1/handoff-reports/projects/update",
    "/v1/handoff-reports/workstreams/register",
    "/v1/handoff-reports/workstreams/list",
    "/v1/handoff-reports/workstreams/update",
    "/v1/handoff-reports/get",
    "/v1/handoff-reports/activities/record",
    "/v1/handoff-reports/activities/list",
    "/v1/handoff-reports/activities/purge",
    "/v1/handoff-reports/workspace-bindings/get",
    "/v1/handoff-reports/workspace-bindings/attach",
    "/v1/handoff-reports/workspace-bindings/detach",
)


class ComparisonFailure(RuntimeError):
    """The two public observations differ."""


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--python-executable", type=Path, required=True)
    parser.add_argument("--python-cwd", type=Path, required=True)
    parser.add_argument("--go-executable", type=Path, required=True)
    parser.add_argument("--go-cwd", type=Path, required=True)
    parser.add_argument("--startup-timeout", type=float, default=45.0)
    return parser.parse_args()


def _free_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def _isolated_environment(home: Path) -> dict[str, str]:
    environment = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith("POWERCONTEXT_")
        and not any(marker in key.upper() for marker in _SECRET_ENV_MARKERS)
    }
    environment.update(
        {
            "POWERCONTEXT_HOME": os.fspath(home),
            "POWERCONTEXT_SERVER_DASHBOARD_ENABLED": "false",
            "POWERCONTEXT_SERVER_LOGGING_ACCESS": "false",
            "POWERCONTEXT_SERVER_LOGGING_FORMAT": "json",
            "POWERCONTEXT_SERVER_LOGGING_LEVEL": "ERROR",
            "POWERCONTEXT_SERVER_MCP_ENABLED": "false",
            "POWERCONTEXT_SERVER_METRICS_ENABLED": "false",
            "POWERCONTEXT_SERVER_TRACING_ENABLED": "false",
        }
    )
    return environment


@contextlib.contextmanager
def _server(
    executable: Path,
    cwd: Path,
    home: Path,
    startup_timeout: float,
) -> Iterator[str]:
    if not executable.is_file():
        raise ComparisonFailure(f"server executable does not exist: {executable}")
    if not cwd.is_dir():
        raise ComparisonFailure(f"server working directory does not exist: {cwd}")
    port = _free_loopback_port()
    base_url = f"http://127.0.0.1:{port}"
    command = (
        os.fspath(executable),
        "server",
        "run",
        "--host",
        "127.0.0.1",
        "--port",
        str(port),
    )
    with tempfile.TemporaryFile() as output:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=_isolated_environment(home),
            stdin=subprocess.DEVNULL,
            stdout=output,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            deadline = time.monotonic() + startup_timeout
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise ComparisonFailure(
                        _process_failure(
                            "server exited during startup", process, output
                        )
                    )
                try:
                    status, _headers, payload = _request(
                        base_url, "GET", "/health/live"
                    )
                except (OSError, URLError):
                    time.sleep(0.05)
                    continue
                if (
                    status == 200
                    and isinstance(payload, dict)
                    and payload.get("status") in {"live", "ok"}
                ):
                    break
                time.sleep(0.05)
            else:
                raise ComparisonFailure(
                    _process_failure("server readiness timed out", process, output)
                )
            yield base_url
        finally:
            if process.poll() is None:
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)


def _process_failure(
    summary: str, process: subprocess.Popen[bytes], output: Any
) -> str:
    output.flush()
    output.seek(0)
    text = output.read().decode("utf-8", errors="replace")[-8_192:]
    return f"{summary} (exit={process.poll()}):\n{text}"


def _request(
    base_url: str,
    method: str,
    path: str,
    payload: Mapping[str, Any] | None = None,
) -> tuple[int, Mapping[str, str], Any]:
    body = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"
    request = Request(base_url + path, data=body, headers=headers, method=method)
    try:
        response = _LOOPBACK_OPENER.open(request, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        status = int(response.status)
        response_headers = {
            key.lower(): value for key, value in response.headers.items()
        }
    try:
        decoded = json.loads(raw) if raw else None
    except json.JSONDecodeError as error:
        raise ComparisonFailure(
            f"{method} {path} returned non-JSON status {status}: {raw[:512]!r}"
        ) from error
    return status, response_headers, decoded


def _observe(
    base_url: str,
    method: str,
    path: str,
    payload: Mapping[str, Any] | None = None,
) -> tuple[dict[str, Any], Any]:
    status, headers, response = _request(base_url, method, path, payload)
    content_type = headers.get("content-type", "").partition(";")[0]
    normalized = _normalize_observation_payload(path, response)
    observation = {
        "status": status,
        "content_type": content_type,
        "cache_control": headers.get("cache-control"),
        "has_request_id": bool(headers.get("x-powercontext-request-id")),
        "payload": normalized,
    }
    return observation, response


def _normalize_observation_payload(path: str, response: Any) -> Any:
    normalized = _normalize_json(response)
    if path == "/v1/capabilities" and isinstance(normalized, dict):
        # The frozen v0.1.0 Oracle predates the Go-only supported-product
        # matrix. Keep its comparison strict for all shared capability fields.
        return {
            key: value
            for key, value in normalized.items()
            if key not in _GO_CAPABILITIES_EXTENSIONS
        }
    return normalized


def _normalize_json(value: Any) -> Any:
    if isinstance(value, list):
        return [_normalize_json(item) for item in value]
    if not isinstance(value, dict):
        return value
    result: dict[str, Any] = {}
    for key, item in value.items():
        if key in _DYNAMIC_JSON_FIELDS:
            result[key] = f"<{key}>"
        elif key == "digest" and isinstance(item, str) and item.startswith("sha256:"):
            result[key] = "<sha256>"
        else:
            result[key] = _normalize_json(item)
    return result


def _scenario(base_url: str) -> dict[str, Any]:
    scope = "differential:scope"
    source_citation = {
        "kind": "source",
        "source_ref": {"name": "content", "source_id": "evidence-1"},
    }
    observations: dict[str, Any] = {}

    observations["live"], _ = _observe(base_url, "GET", "/health/live")
    observations["ready"], _ = _observe(base_url, "GET", "/health/ready")
    observations["capabilities"], _ = _observe(base_url, "GET", "/v1/capabilities")

    # FastAPI validation details are part of the public error contract. Keep
    # representative decode, schema, extra-field, and query failures in the
    # differential so the Go transport cannot regress to a status-only 422.
    for name, payload in (
        ("missing", {}),
        ("string_type", {"scope_id": 7, "query": "query"}),
        ("string_too_short", {"scope_id": "", "query": "query"}),
        ("string_pattern", {"scope_id": "scope", "query": " "}),
        ("extra_forbidden", {"scope_id": "scope", "query": "query", "extra": 1}),
        ("integer_bound", {"scope_id": "scope", "query": "query", "max_bytes": 1}),
        ("integer_type", {"scope_id": "scope", "query": "query", "max_bytes": 1.5}),
    ):
        observations[f"validation:prepare:{name}"], _ = _observe(
            base_url, "POST", "/v1/context/prepare", payload
        )
    observations["validation:prepare:body_missing"], _ = _observe(
        base_url, "POST", "/v1/context/prepare"
    )
    observations["validation:stats:scope_missing"], _ = _observe(
        base_url, "GET", "/v1/stats"
    )
    observations["validation:stats:period_literal"], _ = _observe(
        base_url, "GET", "/v1/stats?scope_id=scope&period=all"
    )

    proposal = {
        "situation": "situation",
        "action": "action",
        "outcome": "outcome",
        "lesson": "lesson",
    }
    source_refs = [
        {"name": "content", "source_id": f"source-{index}"} for index in range(33)
    ]
    artifact_refs = [
        {"family": "experience", "artifact_id": f"artifact-{index}", "revision": 1}
        for index in range(13)
    ]
    for name, path, payload in (
        (
            "string_too_long",
            "/v1/context/prepare",
            {"scope_id": "s" * 257, "query": "query"},
        ),
        (
            "integer_maximum",
            "/v1/context/prepare",
            {"scope_id": "scope", "query": "query", "max_bytes": 32769},
        ),
        (
            "non_nullable_null",
            "/v1/context/prepare",
            {"scope_id": None, "query": "query"},
        ),
        (
            "nested_object_type",
            "/v1/experience/propose",
            {
                "scope_id": "scope",
                "proposal": "invalid",
                "source_refs": [],
                "artifact_refs": [],
            },
        ),
        (
            "array_type",
            "/v1/experience/propose",
            {
                "scope_id": "scope",
                "proposal": proposal,
                "source_refs": "invalid",
                "artifact_refs": [],
            },
        ),
        (
            "array_maximum",
            "/v1/experience/propose",
            {
                "scope_id": "scope",
                "proposal": proposal,
                "source_refs": source_refs,
                "artifact_refs": [],
            },
        ),
        (
            "combined_evidence_maximum",
            "/v1/experience/propose",
            {
                "scope_id": "scope",
                "proposal": proposal,
                "source_refs": source_refs[:20],
                "artifact_refs": artifact_refs,
            },
        ),
        (
            "body_enum",
            "/v1/memory/search",
            {"scope_id": "scope", "query": "query", "mode": "invalid"},
        ),
        (
            "boolean_type",
            "/v1/handoff-reports/get",
            {"scope_id": "scope", "include_evidence_checks": "invalid"},
        ),
    ):
        observations[f"validation:extended:{name}"], _ = _observe(
            base_url, "POST", path, payload
        )

    # Observe every POST operation with the same minimal object. This catches
    # route omissions and validation-envelope drift without inventing defaults
    # outside the public contract. A few list operations legitimately accept
    # the object; the rest must reject it consistently.
    for path in _POST_PATHS:
        key = "minimal:" + path.removeprefix("/v1/").replace("/", ":")
        observations[key], _ = _observe(base_url, "POST", path, {})

    capture = {
        "scope_id": scope,
        "source_id": "evidence-1",
        "content": "The differential acceptance test passed.",
        "metadata": {"kind": "test-output"},
    }
    observations["capture"], _ = _observe(
        base_url, "POST", "/v1/sources/content", capture
    )
    observations["capture_idempotent"], _ = _observe(
        base_url, "POST", "/v1/sources/content", capture
    )
    conflicting_capture = copy.deepcopy(capture)
    conflicting_capture["content"] = "Conflicting content must not replace authority."
    observations["capture_conflict"], _ = _observe(
        base_url, "POST", "/v1/sources/content", conflicting_capture
    )

    contract = {
        "scope_id": scope,
        "source_id": "contract-1",
        "contract": {
            "schema": "powercontext.work-contract.v1",
            "trust": "untrusted_input",
            "objective": "Transfer the implementation safely.",
            "facts": [
                {
                    "text": "The differential acceptance test passed.",
                    "basis": "verified",
                    "evidence": [source_citation],
                }
            ],
            "in_scope": ["Preserve the public behavior."],
            "exclusions": [],
            "completion_criteria": ["Record the receiver outcome."],
            "authorization_notes": [],
            "open_questions": [],
        },
    }
    observations["create_contract"], _ = _observe(
        base_url, "POST", "/v1/work/contracts/create", contract
    )

    current = {
        "scope_id": scope,
        "source_id": "handoff-boundary-1",
        "handoff": {
            "schema": "powercontext.current-work-handoff.v1",
            "trust": "untrusted_input",
            "objective": "Transfer the implementation safely.",
            "state": [
                {
                    "text": "The implementation has passed its differential acceptance test.",
                    "basis": "verified",
                    "evidence": [source_citation],
                }
            ],
            "disposition": "continuable",
            "next_action": {
                "text": "Record the exact receiver outcome.",
                "basis": "declared",
                "evidence": [],
            },
            "omissions": [],
        },
    }
    observations["prepare_current"], prepared = _observe(
        base_url, "POST", "/v1/work/handoffs/prepare-current", current
    )
    if not isinstance(prepared, dict) or not isinstance(prepared.get("handoff"), dict):
        raise ComparisonFailure(
            f"prepare-current did not return a Prepared Handoff: {prepared!r}"
        )

    observations["commit"], committed = _observe(
        base_url,
        "POST",
        "/v1/handoff/commit",
        {"scope_id": scope, "handoff": prepared["handoff"]},
    )
    if not isinstance(committed, dict) or not isinstance(
        committed.get("reference"), dict
    ):
        raise ComparisonFailure("commit did not return a Handoff reference")

    acknowledgement = {
        "scope_id": scope,
        "source_id": "receipt-1",
        "receiver": "receiver-agent",
        "status": "accepted",
        "selection": "exact",
        "receiver_checks": {
            "live_state": "confirmed",
            "capability": "confirmed",
            "authorization": "confirmed",
        },
        "prepared": None,
        "revision": committed["reference"],
        "message": None,
    }
    observations["acknowledge"], acknowledged = _observe(
        base_url, "POST", "/v1/work/handoffs/acknowledge", acknowledgement
    )
    if not isinstance(acknowledged, dict):
        raise ComparisonFailure("acknowledge did not return a response object")
    receipt = acknowledged.get("receipt")
    if not isinstance(receipt, dict) or not isinstance(receipt.get("source"), dict):
        raise ComparisonFailure("acknowledge did not return a Receipt source")

    outcome = {
        "scope_id": scope,
        "source_id": "outcome-1",
        "outcome": {
            "schema": "powercontext.task-outcome.v1",
            "trust": "untrusted_observation",
            "objective": "Transfer the implementation safely.",
            "status": "succeeded",
            "summary": "The receiver preserved the public behavior.",
            "handoff_receipt_ref": receipt["source"],
            "observations": [
                {
                    "text": "The receiver completed the exact acceptance run.",
                    "basis": "declared",
                    "evidence": [],
                }
            ],
            "checks": [],
            "produced_artifacts": [],
            "remaining_work": [],
        },
    }
    observations["record_outcome"], _ = _observe(
        base_url, "POST", "/v1/work/outcomes/record", outcome
    )

    observations["known_scopes"], _ = _observe(
        base_url, "POST", "/v1/handoff-reports/scopes/list-known", {}
    )
    observations["scope_report"], _ = _observe(
        base_url,
        "POST",
        "/v1/handoff-reports/get",
        {"scope_id": scope, "format": "json", "include_evidence_checks": True},
    )
    observations["stats"], _ = _observe(
        base_url,
        "GET",
        f"/v1/stats?scope_id={quote(scope, safe='')}&period=today",
    )

    missing_contract = copy.deepcopy(contract)
    missing_contract["source_id"] = "contract-missing-evidence"
    missing_contract["contract"]["facts"][0]["evidence"][0]["source_ref"][
        "source_id"
    ] = "missing"
    observations["missing_contract_evidence"], _ = _observe(
        base_url, "POST", "/v1/work/contracts/create", missing_contract
    )
    return observations


def _run_one(
    executable: Path,
    cwd: Path,
    root: Path,
    startup_timeout: float,
) -> dict[str, Any]:
    home = root / "home"
    home.mkdir(parents=True, mode=0o700)
    with _server(
        executable.resolve(), cwd.resolve(), home, startup_timeout
    ) as base_url:
        return _scenario(base_url)


def _canonical(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n"


def main() -> int:
    args = _parse_args()
    with tempfile.TemporaryDirectory(prefix="powercontext-differential-") as temporary:
        root = Path(temporary)
        python_observation = _run_one(
            args.python_executable,
            args.python_cwd,
            root / "python",
            args.startup_timeout,
        )
        go_observation = _run_one(
            args.go_executable,
            args.go_cwd,
            root / "go",
            args.startup_timeout,
        )
    if python_observation != go_observation:
        difference = "".join(
            difflib.unified_diff(
                _canonical(python_observation).splitlines(keepends=True),
                _canonical(go_observation).splitlines(keepends=True),
                fromfile="python-3a6cb015",
                tofile="go",
            )
        )
        raise ComparisonFailure(
            "Python and Go public observations differ:\n" + difference
        )
    print(f"differential HTTP scenarios matched ({len(go_observation)} observations)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ComparisonFailure as error:
        print(error, file=sys.stderr)
        raise SystemExit(1) from None
