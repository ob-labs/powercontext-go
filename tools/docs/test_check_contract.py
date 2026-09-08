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

from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


CHECKER = Path(__file__).with_name("check_contract.py")


class DocumentationContractTest(unittest.TestCase):
    def test_mismatched_documented_go_version_is_rejected(self) -> None:
        result = self.run_checker(
            go_version="1.27.0",
            index_version="1.25",
            architecture="# Architecture\n",
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn(
            "docs/index.md: documented Go version 1.25 does not match go.mod 1.27.0",
            result.stderr,
        )

    def test_documentation_conflict_marker_is_rejected(self) -> None:
        result = self.run_checker(
            go_version="1.27.0",
            index_version="1.27.0",
            architecture="# Architecture\n<<<<<<< HEAD\ncurrent\n=======\nincoming\n>>>>>>> branch\n",
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn(
            "docs/architecture/README.md:2: unresolved Git conflict marker",
            result.stderr,
        )

    def test_matching_clean_documentation_is_accepted(self) -> None:
        result = self.run_checker(
            go_version="1.27.0",
            index_version="1.27.0",
            architecture="# Architecture\n",
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")

    def test_missing_e4_two_pin_policy_is_rejected(self) -> None:
        result = self.run_checker(
            go_version="1.27.0",
            index_version="1.27.0",
            architecture="# Architecture\n",
            openapi_readme="# OpenAPI\n\n77 canonical operations and 38 deferred entries.\n",
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn("openapi/README.md: missing E4 two-pin policy", result.stderr)

    def test_correct_policy_with_obsolete_single_pin_inventory_is_rejected(self) -> None:
        claims = (
            "The canonical ledger contains 77 canonical operations.",
            "The canonical ledger contains 38 upstream-only operations.",
            "The two-pin ledger retains 38 deferred entries.",
        )

        for claim in claims:
            with self.subTest(claim=claim):
                result = self.run_checker(
                    go_version="1.27.0",
                    index_version="1.27.0",
                    architecture="# Architecture\n",
                    openapi_readme=self.valid_openapi_readme() + "\n" + claim,
                )

                self.assertEqual(result.returncode, 1)
                self.assertIn(
                    "openapi/README.md: contains obsolete 77/38 single-pin inventory",
                    result.stderr,
                )

    def test_correct_policy_with_false_e4_capability_claims_is_rejected(self) -> None:
        claims = {
            "Access": "The e4 Access endpoint is available.",
            "Prompt": "The e4 Prompt behavior is supported.",
            "Artifact tags": "The e4 Artifact tag schema behavior is implemented.",
            "Artifact revision-history": "The e4 Artifact revision-history read is now available.",
            "Source receipt": "The e4 Source receipt is implemented.",
            "Artifact writes": "Artifact writes are supported.",
            "managed Skills": "Managed Skills are available.",
            "remote Skills": "Remote Skills now support installation.",
            "native personal services": "Native personal services provide e4 targets.",
        }

        for capability, claim in claims.items():
            with self.subTest(capability=capability):
                result = self.run_checker(
                    go_version="1.27.0",
                    index_version="1.27.0",
                    architecture="# Architecture\n",
                    openapi_readme=self.valid_openapi_readme() + "\n" + claim,
                )

                self.assertEqual(result.returncode, 1)
                self.assertIn(
                    f"openapi/README.md: falsely claims implemented {capability}",
                    result.stderr,
                )

    def test_e4_deferral_and_negation_language_is_allowed(self) -> None:
        permitted = (
            "The e4 Access cluster remains deferred. "
            "The e4 Prompt behavior is not implemented. "
            "The e4 Artifact tag schema does not implement writes. "
            "The e4 Artifact revision-history read remains deferred. "
            "The e4 Source receipt identity is not available. "
            "Artifact writes are not supported. "
            "Managed Skills remain deferred. "
            "Remote Skills do not provide targets. "
            "Native personal services are not implemented."
        )
        result = self.run_checker(
            go_version="1.27.0",
            index_version="1.27.0",
            architecture="# Architecture\n",
            openapi_readme=self.valid_openapi_readme() + "\n" + permitted,
        )

        self.assertEqual(result.returncode, 0, result.stderr)

    def run_checker(
        self,
        *,
        go_version: str,
        index_version: str,
        architecture: str,
        openapi_readme: str | None = None,
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text(
                f"module example.com/docs-contract\n\ngo {go_version}\n",
                encoding="utf-8",
            )
            (root / "docs" / "architecture").mkdir(parents=True)
            (root / "docs" / "index.md").write_text(
                f"# Example\n\nExample is the Go {index_version} implementation.\n",
                encoding="utf-8",
            )
            (root / "docs" / "architecture" / "README.md").write_text(
                architecture,
                encoding="utf-8",
            )
            (root / "openapi").mkdir()
            (root / "openapi" / "README.md").write_text(
                openapi_readme or self.valid_openapi_readme(),
                encoding="utf-8",
            )
            return subprocess.run(
                [sys.executable, str(CHECKER), "--root", str(root)],
                check=False,
                capture_output=True,
                text=True,
            )

    @staticmethod
    def valid_openapi_readme() -> str:
        return "\n".join(
            (
                "The current-master inventory is pinned to `oceanbase/powercontext@e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c`: 94 canonical operations, 55 upstream-only operations, and 17 newly deferred operations.",
                "Scope, Source, and Artifact sidecars remain independently pinned to `oceanbase/powercontext@74b961fbb07165595314726715d412a3d0d90589`.",
                "The e4 Artifact revision-history, Artifact tag, Prompt, and Access clusters remain deferred. This rebaseline does not implement Artifact writes, managed Skills, remote Skills, or native personal services.",
            )
        )


if __name__ == "__main__":
    unittest.main()
