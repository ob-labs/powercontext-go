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
E4_OPENAPI_POLICY = (
    "The current-master inventory is pinned to "
    "`oceanbase/powercontext@e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c`: "
    "94 canonical operations, 55 upstream-only operations, and 17 newly "
    "deferred operations."
)
SIDECAR_PIN_POLICY = (
    "Scope, Source, and Artifact sidecars remain independently pinned to "
    "`oceanbase/powercontext@74b961fbb07165595314726715d412a3d0d90589`."
)
DEFERRED_E4_POLICY = (
    "The e4 Artifact revision-history, Artifact tag, Prompt, and Access "
    "clusters remain deferred. This rebaseline does not implement Artifact "
    "writes, managed Skill generation or lifecycle, remote Skills, or native "
    "personal services."
)
LEGACY_SINGLE_PIN_PATTERNS = (
    re.compile(r"\b77\s+canonical\s+operations\b", re.IGNORECASE),
    re.compile(r"\b38\s+(?:pinned\s+)?upstream-only\s+operations\b", re.IGNORECASE),
    re.compile(r"\b38\s+deferred\s+entries\b", re.IGNORECASE),
)


def affirmative_claim_pattern(category: str) -> re.Pattern[str]:
    description = r"(?:\s+(?:cluster|schema|behavior|endpoints?|operations?|read|writes?|identity))*"
    predicate = (
        r"(?:is|are)\s+(?:now\s+)?(?:implemented|supported|available)"
        r"|(?:now\s+)?(?:supports?|provides?|enables?)"
    )
    return re.compile(rf"\b{category}{description}\s+(?:{predicate})\b", re.IGNORECASE)


PROHIBITED_E4_CLAIMS = {
    "Access": affirmative_claim_pattern(r"e4\s+Access"),
    "Prompt": affirmative_claim_pattern(r"e4\s+Prompt"),
    "Artifact tags": affirmative_claim_pattern(r"e4\s+Artifact\s+tags?"),
    "Artifact revision-history": affirmative_claim_pattern(
        r"e4\s+Artifact\s+revision-history"
    ),
    "Source receipt": affirmative_claim_pattern(r"e4\s+Source\s+receipt"),
    "Artifact writes": affirmative_claim_pattern(r"Artifact\s+writes?"),
    "managed Skills": affirmative_claim_pattern(r"managed\s+Skills?"),
    "managed Skill generation": affirmative_claim_pattern(
        r"managed\s+Skills?\s+generation"
    ),
    "managed Skill lifecycle": affirmative_claim_pattern(
        r"managed\s+Skills?\s+lifecycle"
    ),
    "managed Skill remote distribution": affirmative_claim_pattern(
        r"managed\s+Skills?\s+(?:package\s+)?remote(?:\s+distribution)?"
    ),
    "remote Skills": affirmative_claim_pattern(r"remote\s+Skills?"),
    "native personal services": affirmative_claim_pattern(
        r"native\s+personal\s+services?"
    ),
}


def contains_policy(text: str, policy: str) -> bool:
    pattern = re.escape(policy).replace(r"\ ", r"\s+")
    return re.search(pattern, text) is not None


def documentation_errors(root: Path) -> list[str]:
    errors: list[str] = []
    go_mod = (root / "go.mod").read_text(encoding="utf-8")
    index_path = root / "docs" / "index.md"
    index = index_path.read_text(encoding="utf-8")
    openapi_path = root / "openapi" / "README.md"

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

    if not openapi_path.is_file():
        errors.append("openapi/README.md: missing E4 two-pin policy")
    else:
        openapi = openapi_path.read_text(encoding="utf-8")
        for policy in (E4_OPENAPI_POLICY, SIDECAR_PIN_POLICY, DEFERRED_E4_POLICY):
            if not contains_policy(openapi, policy):
                errors.append("openapi/README.md: missing E4 two-pin policy")
                break
        for pattern in LEGACY_SINGLE_PIN_PATTERNS:
            if pattern.search(openapi):
                errors.append("openapi/README.md: contains obsolete 77/38 single-pin inventory")
                break
        for capability, pattern in PROHIBITED_E4_CLAIMS.items():
            if pattern.search(openapi):
                errors.append(f"openapi/README.md: falsely claims implemented {capability}")

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
