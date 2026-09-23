"""Tests for gate_precision_reset.sh."""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "gate_precision_reset.sh"


def _git(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        check=True,
        capture_output=True,
        text=True,
    )


def _init_repo(repo: Path) -> None:
    repo.mkdir()
    subprocess.run(
        ["git", "init", "-b", "main", str(repo)],
        check=True,
        capture_output=True,
        text=True,
    )
    _git(repo, "config", "user.email", "test@example.com")
    _git(repo, "config", "user.name", "Test")
    _git(repo, "config", "commit.gpgsign", "false")


def _commit_base(repo: Path) -> str:
    (repo / "README").write_text("base\n")
    (repo / ".gitignore").write_text("ignored.txt\n")
    _git(repo, "add", "README", ".gitignore")
    _git(repo, "commit", "-m", "base")
    return _git(repo, "rev-parse", "HEAD").stdout.strip()


def _run(repo: Path, sha: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT), str(repo), sha],
        capture_output=True,
        text=True,
        check=False,
    )


def test_reset_restores_pristine_base(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    _init_repo(repo)
    base = _commit_base(repo)

    (repo / "README").write_text("later\n")
    _git(repo, "add", "README")
    _git(repo, "commit", "-m", "later")
    # aoa leaves the attempt branch checked out in a worktree, not as a bare ref.
    _git(repo, "worktree", "add", "-b", "aoa/x", str(tmp_path / "aoa-wt"))
    (repo / "untracked.txt").write_text("untracked\n")
    (repo / "ignored.txt").write_text("ignored\n")

    result = _run(repo, base)

    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    assert result.stderr == ""
    assert _git(repo, "rev-parse", "HEAD").stdout.strip() == base
    assert _git(repo, "rev-parse", "--abbrev-ref", "HEAD").stdout.strip() == "main"
    assert (repo / "README").read_text() == "base\n"
    status = _git(repo, "status", "--porcelain", "--ignored").stdout
    assert status.strip() == ""
    branches = _git(
        repo, "for-each-ref", "--format=%(refname:short)", "refs/heads"
    ).stdout.split()
    assert branches == ["main"]
    listed = _git(repo, "worktree", "list", "--porcelain").stdout
    assert listed.count("worktree ") == 1


def test_reset_prunes_deleted_worktree(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    _init_repo(repo)
    base = _commit_base(repo)
    wt = tmp_path / "aoa-wt"
    _git(repo, "worktree", "add", "-b", "aoa/x", str(wt))
    shutil.rmtree(wt)

    result = _run(repo, base)

    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    assert result.stderr == ""
    assert _git(repo, "rev-parse", "HEAD").stdout.strip() == base
    branches = _git(
        repo, "for-each-ref", "--format=%(refname:short)", "refs/heads"
    ).stdout.split()
    assert branches == ["main"]
    listed = _git(repo, "worktree", "list", "--porcelain").stdout
    assert listed.count("worktree ") == 1


def test_reset_rejects_bad_sha(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    _init_repo(repo)
    _commit_base(repo)

    result = _run(repo, "0123456789abcdef0123456789abcdef01234567")

    assert result.returncode != 0
    assert result.stderr.strip() != ""


def test_reset_rejects_non_repository(tmp_path: Path) -> None:
    repo = tmp_path / "not-a-repo"
    repo.mkdir()

    result = _run(repo, "0123456789abcdef0123456789abcdef01234567")

    assert result.returncode != 0
    assert result.stderr.strip() != ""
