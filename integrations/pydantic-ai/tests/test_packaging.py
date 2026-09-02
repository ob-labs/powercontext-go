# Copyright (c) 2026 OceanBase.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

"""Installed-wheel smoke tests for the Pydantic AI adapter."""

from __future__ import annotations

import os
import re
import subprocess
import sys
from pathlib import Path
from shutil import which

_PROJECT = Path(__file__).resolve().parents[1]


def _build_wheel(out_dir: Path) -> Path:
    uv = which("uv")
    assert uv is not None
    result = subprocess.run([uv, "build", "--wheel", "--out-dir", str(out_dir), str(_PROJECT)], capture_output=True, text=True, timeout=300)
    assert result.returncode == 0, f"uv build failed:\n{result.stdout}\n{result.stderr}"
    wheels = list(out_dir.glob("*.whl"))
    assert len(wheels) == 1
    return wheels[0]


def test_documented_openai_agent_constructs_from_installed_wheels(tmp_path: Path) -> None:
    wheel = _build_wheel(tmp_path / "wheel")
    uv = which("uv")
    assert uv is not None
    site_packages = tmp_path / "site-packages"
    install = subprocess.run([uv, "pip", "install", "--target", str(site_packages), "powercontext==0.1.0", "pydantic-ai-slim[openai]", str(wheel)], capture_output=True, text=True, timeout=300)
    assert install.returncode == 0, f"wheel install failed:\n{install.stdout}\n{install.stderr}"
    match = re.search(r"```python\n(?P<code>.*?)```", (_PROJECT / "README.md").read_text(encoding="utf-8"), re.DOTALL)
    assert match is not None
    environment = os.environ.copy()
    environment["OPENAI_API_KEY"] = "docs-smoke-test-key"
    smoke = subprocess.run([sys.executable, "-I", "-c", f"import sys\nsys.path.insert(0, {str(site_packages)!r})\n{match.group('code')}\nassert agent is not None"], cwd=tmp_path, env=environment, capture_output=True, text=True, timeout=60)
    assert smoke.returncode == 0, f"documented example failed:\n{smoke.stdout}\n{smoke.stderr}"
