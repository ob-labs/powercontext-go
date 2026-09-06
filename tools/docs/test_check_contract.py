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

    def run_checker(
        self,
        *,
        go_version: str,
        index_version: str,
        architecture: str,
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
            return subprocess.run(
                [sys.executable, str(CHECKER), "--root", str(root)],
                check=False,
                capture_output=True,
                text=True,
            )


if __name__ == "__main__":
    unittest.main()
