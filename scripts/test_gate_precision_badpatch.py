"""Tests for gate_precision_badpatch.sh."""
from __future__ import annotations

import runpy
import subprocess
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent / "gate_precision_badpatch.sh"


def _run(cwd: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT), *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )


def _init_git(cwd: Path) -> None:
    subprocess.run(
        ["git", "init"],
        cwd=cwd,
        check=True,
        capture_output=True,
        text=True,
    )


def test_writes_conftest_and_exits_zero(tmp_path: Path) -> None:
    _init_git(tmp_path)
    proc = _run(tmp_path, "dummy prompt")
    assert proc.returncode == 0, proc.stderr
    conftest = tmp_path / "conftest.py"
    assert conftest.is_file()
    assert sorted(p.name for p in tmp_path.iterdir() if p.name != ".git") == ["conftest.py"]
    with pytest.raises(RuntimeError, match="deliberate bad patch"):
        runpy.run_path(str(conftest))


def test_refuses_existing_conftest(tmp_path: Path) -> None:
    _init_git(tmp_path)
    conftest = tmp_path / "conftest.py"
    original = "# keep me\n"
    conftest.write_text(original)
    proc = _run(tmp_path, "dummy prompt")
    assert proc.returncode != 0
    assert "conftest.py" in proc.stderr
    assert conftest.read_text() == original
