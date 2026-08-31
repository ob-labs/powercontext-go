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

"""Generate and verify the frozen Python v0.0.2 SQLite authority fixture.

This file lives in the Go repository so the Python repository remains an
unmodified Oracle. It is executed with the frozen repository's locked virtual
environment and imports only public PowerContext APIs for writes and reads.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import hashlib
import json
import sqlite3
from pathlib import Path
from types import SimpleNamespace
from typing import Any

from powercontext.builtin.artifacts.memory import MemoryCandidateRequest, MemoryEntryInput
from powercontext.builtin.persistence.sqlite import SQLiteConfig
from powercontext.builtin.runtime import BuiltinConfig, open_builtin_contexts
from powercontext.builtin.sources import ContentCapture, ContentSource

SCOPE_ID = "project:python-oracle"
SOURCE_ID = "capture-python-1"
GO_SOURCE_ID = "capture-go-1"


class EchoCandidatePipeline:
    async def extract(self, request: MemoryCandidateRequest, /) -> tuple[MemoryEntryInput, ...]:
        return tuple(
            MemoryEntryInput(kind="decision", text=source.content, sources=(source,))
            for source in request.sources
            if isinstance(source, ContentSource)
        )


def install_deterministic_ids() -> None:
    # RelationalContexts creates every built-in entry/version identifier through
    # its scoped UUID factory. Replacing only this module-local dependency keeps
    # production code untouched and makes the committed database reproducible.
    from powercontext.builtin.runtime import relational

    counter = 0

    def next_uuid() -> SimpleNamespace:
        nonlocal counter
        counter += 1
        return SimpleNamespace(hex=f"{counter:032x}")

    relational.uuid4 = next_uuid


async def generate(database_path: Path) -> None:
    install_deterministic_ids()
    for candidate in (database_path, Path(f"{database_path}-shm"), Path(f"{database_path}-wal")):
        candidate.unlink(missing_ok=True)
    config = BuiltinConfig(
        database=SQLiteConfig(
            url=f"sqlite+aiosqlite:///{database_path}",
            journal_mode="WAL",
        )
    )
    async with open_builtin_contexts(config, candidate_pipeline=EchoCandidatePipeline()) as contexts:
        context = await contexts.get(SCOPE_ID)
        source, position = await context.sources.capture(
            ContentCapture(
                source_id=SOURCE_ID,
                content="Use one atomic café composition boundary.",
                metadata={"origin": "python", "nested": {"stable": True}},
            )
        )
        assert source.name == SOURCE_ID and position == 1
        result = await context.triggers.flush(limit=10)
        assert result.previous_cursor == 0 and result.current_cursor == 1
        memory = await context.artifacts.memory.head("memory")
        entries = await context.artifacts.memory.entries(memory)
        assert memory.revision == 1 and len(entries) == 1

    # Produce a self-contained fixture instead of relying on a sidecar WAL.
    with sqlite3.connect(database_path) as connection:
        connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        connection.execute("PRAGMA journal_mode=DELETE")
        connection.execute("VACUUM")


def encoded_cell(value: Any) -> Any:
    if isinstance(value, bytes):
        return {"base64": base64.b64encode(value).decode("ascii")}
    return value


def semantic_snapshot(database_path: Path, baseline: str) -> dict[str, Any]:
    result: dict[str, Any] = {"schema": f"powercontext.python-{baseline}.sqlite-authority.v2", "tables": {}}
    with sqlite3.connect(database_path) as connection:
        schema_objects = [
            {"type": row[0], "name": row[1], "sql": row[2]}
            for row in connection.execute(
                "SELECT type, name, sql FROM sqlite_master "
                "WHERE name LIKE 'pc_%' ORDER BY type, name"
            )
        ]
        schema_bytes = json.dumps(
            schema_objects,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
        result["schema_objects"] = schema_objects
        result["schema_sha256"] = hashlib.sha256(schema_bytes).hexdigest()
        table_names = [
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'pc_%' ORDER BY name"
            )
        ]
        for table in table_names:
            quoted = '"' + table.replace('"', '""') + '"'
            columns = [row[1] for row in connection.execute(f"PRAGMA table_info({quoted})")]
            rows = [
                [encoded_cell(cell) for cell in row]
                for row in connection.execute(f"SELECT * FROM {quoted}")
            ]
            rows.sort(key=lambda row: json.dumps(row, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
            result["tables"][table] = {"columns": columns, "rows": rows}
    return result


async def verify(database_path: Path) -> None:
    config = BuiltinConfig(database=SQLiteConfig(url=f"sqlite+aiosqlite:///{database_path}"))
    async with open_builtin_contexts(config) as contexts:
        context = await contexts.get(SCOPE_ID)
        sources = await context.sources.list()
        by_name = {source.name: source for source in sources if isinstance(source, ContentSource)}
        assert by_name[SOURCE_ID].content == "Use one atomic café composition boundary."
        assert by_name[GO_SOURCE_ID].content == "Go wrote this row into the Python authority database."
        memory = await context.artifacts.memory.head("memory")
        entries = await context.artifacts.memory.entries(memory)
        assert memory.revision == 1
        assert tuple(entry.text for entry in entries) == ("Use one atomic café composition boundary.",)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("generate", "verify"))
    parser.add_argument("database", type=Path)
    parser.add_argument("--snapshot", type=Path)
    parser.add_argument("--baseline", default="v0.0.2")
    arguments = parser.parse_args()
    if arguments.mode == "generate":
        asyncio.run(generate(arguments.database))
        if arguments.snapshot is None:
            parser.error("generate requires --snapshot")
        snapshot = semantic_snapshot(arguments.database, arguments.baseline)
        arguments.snapshot.write_text(
            json.dumps(snapshot, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
            newline="\n",
        )
        return
    asyncio.run(verify(arguments.database))


if __name__ == "__main__":
    main()
