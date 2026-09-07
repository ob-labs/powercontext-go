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

"""Validate documentation facts that the renderer cannot detect."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


GO_DIRECTIVE = re.compile(r"^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", re.MULTILINE)
DOCUMENTED_GO_VERSION = re.compile(
    r"\bGo (\d+\.\d+(?:\.\d+)?) implementation\b"
)
CONFLICT_BOUNDARY = re.compile(r"^(?:<<<<<<<|>>>>>>>) .+$")


def documentation_errors(root: Path) -> list[str]:
    errors: list[str] = []
    go_mod = (root / "go.mod").read_text(encoding="utf-8")
    index_path = root / "docs" / "index.md"
    index = index_path.read_text(encoding="utf-8")

    go_match = GO_DIRECTIVE.search(go_mod)
    if go_match is None:
        errors.append("go.mod: missing Go version directive")
    else:
        documented_match = DOCUMENTED_GO_VERSION.search(index)
        if documented_match is None:
            errors.append("docs/index.md: missing documented Go implementation version")
        elif documented_match.group(1) != go_match.group(1):
            errors.append(
                "docs/index.md: documented Go version "
                f"{documented_match.group(1)} does not match go.mod {go_match.group(1)}"
            )

    for path in sorted((root / "docs").rglob("*.md")):
        for line_number, line in enumerate(
            path.read_text(encoding="utf-8").splitlines(), start=1
        ):
            if CONFLICT_BOUNDARY.fullmatch(line):
                relative_path = path.relative_to(root).as_posix()
                errors.append(
                    f"{relative_path}:{line_number}: unresolved Git conflict marker"
                )

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path.cwd())
    args = parser.parse_args()

    errors = documentation_errors(args.root)
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
