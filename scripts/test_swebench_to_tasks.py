"""--gate=repo for repos whose PASS_TO_PASS ids are not pytest node ids.

Pytest node ids (they contain ``::``) keep the historical file-level pytest
gate. Django and sympy ids do not, so the gate runs the test files named in
the held-out test patch, with the repo's own test command, then grades the
log per PASS_TO_PASS id.
"""

from __future__ import annotations

import json
import os
import shlex
import subprocess
import sys
from pathlib import Path

import pytest
import swebench_to_tasks as s

DJANGO_CMD = "./tests/runtests.py --verbosity 2 --settings=test_sqlite --parallel 1"
SYMPY_CMD = (
    "PYTHONWARNINGS='ignore::UserWarning,ignore::SyntaxWarning' "
    "bin/test -C --verbose"
)


def _patch(paths: list[str], body: str = "") -> str:
    """A diff whose headers name paths. body is hunk text, never a header."""
    chunks: list[str] = []
    for path in paths:
        chunks.append(
            f"diff --git a/{path} b/{path}\n"
            f"--- a/{path}\n"
            f"+++ b/{path}\n"
            "@@ -1 +1,2 @@\n"
            " keep\n"
            f"+{body}\n"
        )
    return "".join(chunks)


def _row(**overrides: object) -> dict[str, object]:
    row: dict[str, object] = {
        "instance_id": "repo__proj-1",
        "repo": "django/django",
        "version": "4.2",
        "base_commit": "abc123",
        "problem_statement": "fix the bug",
        "FAIL_TO_PASS": ["test_held_out"],
        "PASS_TO_PASS": ["test_abs"],
        "test_patch": "",
    }
    row.update(overrides)
    return row


def _write(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    rows: list[dict[str, object]],
    present: dict[str, list[str]],
    test_cmd: str | None,
) -> str:
    """Run the adapter with prepare_repo stubbed and test_cmd injected."""
    inst = tmp_path / "instances.json"
    inst.write_text(json.dumps(rows))
    out = tmp_path / "tasks.toml"
    work = tmp_path / "work"

    def fake_prepare(
        workdir: str, instance_id: str, repo_name: str, base_commit: str
    ) -> str:
        del repo_name, base_commit
        dest = Path(workdir) / instance_id
        dest.mkdir(parents=True, exist_ok=True)
        for rel in present.get(instance_id, []):
            path = dest.joinpath(*rel.split("/"))
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("ok\n")
        return str(dest)

    def fake_lookup(repo: str, version: str) -> str:
        del repo, version
        assert test_cmd is not None
        return test_cmd

    monkeypatch.setattr(s, "prepare_repo", fake_prepare)
    if test_cmd is not None:
        monkeypatch.setattr(s, "lookup_test_cmd", fake_lookup, raising=False)
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "swebench_to_tasks.py",
            str(inst),
            str(work),
            str(out),
            "--gate",
            "repo",
        ],
    )
    s.main()
    return out.read_text()


def _gate_lines(text: str) -> list[str]:
    return [line for line in text.splitlines() if line.startswith("gate = ")]


def _bash_n(script: str) -> None:
    proc = subprocess.run(
        ["bash", "-n"],
        input=script,
        text=True,
        capture_output=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr


def _git(cwd: Path, *args: str) -> str:
    proc = subprocess.run(
        ["git", "-C", str(cwd), *args],
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        raise AssertionError(proc.stderr or proc.stdout)
    return proc.stdout.strip()


def test_django_gate_uses_test_patch_module(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """One command: test_cmd plus the django module, never the .txt file."""
    iid = "django__django-1"
    text = _write(
        tmp_path,
        monkeypatch,
        [
            _row(
                instance_id=iid,
                repo="django/django",
                PASS_TO_PASS=["test_abs", "test_foo (migrations.test_operations.Tests)"],
                FAIL_TO_PASS=["test_held_out (migrations.test_operations.Tests)"],
                test_patch=_patch(
                    [
                        "tests/migrations/test_operations.py",
                        "docs/x.txt",
                    ],
                    body="see tests/decoy_only_in_body.py",
                ),
            )
        ],
        {
            iid: [
                "tests/migrations/test_operations.py",
                "docs/x.txt",
                "tests/decoy_only_in_body.py",
            ]
        },
        DJANGO_CMD,
    )
    gates = _gate_lines(text)
    commands = s.repo_gate_commands(
        {
            "repo": "django/django",
            "version": "4.2",
            "PASS_TO_PASS": [
                "test_abs",
                "test_foo (migrations.test_operations.Tests)",
            ],
            "FAIL_TO_PASS": ["test_held_out (migrations.test_operations.Tests)"],
            "test_patch": _patch(
                [
                    "tests/migrations/test_operations.py",
                    "docs/x.txt",
                ],
                body="see tests/decoy_only_in_body.py",
            ),
        },
        str(tmp_path / "work" / iid),
        DJANGO_CMD,
    )
    assert commands is not None
    assert gates == ["gate = " + s.toml_cmd_list(commands)]
    script = commands[0][2]
    assert commands[0].count("/bin/bash") == 1
    assert f"{DJANGO_CMD} migrations.test_operations" in script
    head = script.split("python - /tmp/aoa-gate.log", 1)[0]
    assert "x.txt" not in script
    assert "decoy_only_in_body" not in script
    assert "python -m pytest" not in script
    assert "test_held_out" not in script
    assert "test_abs" not in head


def test_sympy_gate_uses_test_patch_path(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Sympy keeps the source path and runs it with the repo test command."""
    iid = "sympy__sympy-1"
    text = _write(
        tmp_path,
        monkeypatch,
        [
            _row(
                instance_id=iid,
                repo="sympy/sympy",
                version="1.12",
                PASS_TO_PASS=["test_abs"],
                test_patch=_patch(["sympy/matrices/tests/test_commonmatrix.py"]),
            )
        ],
        {iid: ["sympy/matrices/tests/test_commonmatrix.py"]},
        SYMPY_CMD,
    )
    gates = _gate_lines(text)
    assert len(gates) == 1
    assert gates[0].count("/bin/bash") == 1
    assert f"{SYMPY_CMD} sympy/matrices/tests/test_commonmatrix.py" in gates[0]
    assert "python -m pytest" not in gates[0]
    assert "cp -a /workspace/." not in gates[0]


def test_missing_file_dropped_and_empty_instance_skipped(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    """A file the patch creates is dropped; an instance with none left is skipped."""
    kept = "django__django-kept"
    empty = "django__django-empty"
    text = _write(
        tmp_path,
        monkeypatch,
        [
            _row(
                instance_id=kept,
                repo="django/django",
                PASS_TO_PASS=["test_abs"],
                test_patch=_patch(
                    [
                        "tests/migrations/test_created.py",
                        "tests/migrations/test_operations.py",
                    ]
                ),
            ),
            _row(
                instance_id=empty,
                repo="django/django",
                PASS_TO_PASS=["test_abs"],
                test_patch=_patch(["tests/migrations/test_created.py"]),
            ),
        ],
        {kept: ["tests/migrations/test_operations.py"], empty: []},
        DJANGO_CMD,
    )
    err = capsys.readouterr().err
    gates = _gate_lines(text)
    assert len(gates) == 1
    assert "migrations.test_operations" in gates[0]
    assert "test_created" not in gates[0]
    assert f"name = {s.toml_str(kept)}" in text
    assert empty not in text
    assert (
        f"skipping {empty}: --gate=repo needs test files present at base_commit"
        in err
    )


def test_pytest_shaped_instance_keeps_today_gate(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Ids containing ``::`` still produce the historical conda pytest gate."""
    iid = "astropy__astropy-1"
    f2p = ["tests/test_foo.py::test_new"]
    p2p = ["tests/test_foo.py::test_old", "tests/test_foo.py::test_other"]
    text = _write(
        tmp_path,
        monkeypatch,
        [
            _row(
                instance_id=iid,
                repo="astropy/astropy",
                version="4.3",
                PASS_TO_PASS=p2p,
                FAIL_TO_PASS=f2p,
                test_patch=_patch(["tests/test_foo.py"]),
            )
        ],
        {iid: ["tests/test_foo.py"]},
        "SHOULD_NOT_RUN",
    )
    expected = s.conda_pytest(["tests/test_foo.py"], f2p)
    assert _gate_lines(text) == ["gate = " + s.toml_cmd_list([expected])]
    shell = expected[2]
    assert shell.endswith(
        "python -m pytest -q tests/test_foo.py "
        "--deselect tests/test_foo.py::test_new"
    )
    assert "cp -a /workspace/." not in shell
    assert s.COPY_WORKTREE in shell
    assert "<<'EOF'" not in shell
    assert "parse_log_" not in shell
    assert "SHOULD_NOT_RUN" not in text
    _bash_n(shell)


def test_nonpytest_gate_embeds_grader_and_skips_git(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The django/sympy Gate grades the log and does not copy .git."""
    iid = "django__django-1"
    p2p = ["test_abs", "test_foo (migrations.test_operations.Tests)"]
    text = _write(
        tmp_path,
        monkeypatch,
        [
            _row(
                instance_id=iid,
                repo="django/django",
                PASS_TO_PASS=p2p,
                test_patch=_patch(["tests/migrations/test_operations.py"]),
            )
        ],
        {iid: ["tests/migrations/test_operations.py"]},
        DJANGO_CMD,
    )
    script = s.repo_gate_commands(
        {
            "repo": "django/django",
            "version": "4.2",
            "PASS_TO_PASS": p2p,
            "test_patch": _patch(["tests/migrations/test_operations.py"]),
        },
        str(tmp_path / "work" / iid),
        DJANGO_CMD,
    )
    assert script is not None
    assert _gate_lines(text) == ["gate = " + s.toml_cmd_list(script)]
    shell = script[0][2]
    runner = (
        "{ set +e; set +o pipefail; { "
        + DJANGO_CMD
        + " migrations.test_operations; } 2>&1 | tee /tmp/aoa-gate.log; "
    )
    assert runner in shell
    assert shell.index(runner) < shell.index("python - /tmp/aoa-gate.log ")
    assert (
        "python - /tmp/aoa-gate.log "
        + shlex.quote("django/django")
        + " "
        + shlex.quote(json.dumps(p2p))
        + " <<'EOF'\n"
    ) in shell
    assert s.grader_source() in shell
    assert "parse_log_django" in shell
    assert shell.rstrip().endswith("}")
    assert "cp -a /workspace/." not in shell
    assert '[ "$entry" = .git ]' in shell
    assert s.COPY_WORKTREE in shell
    _bash_n(shell)


def test_pytest_gate_does_not_copy_git() -> None:
    """The pytest-path Gate copies the worktree without .git."""
    shell = s.conda_pytest(
        ["tests/test_foo.py"], ["tests/test_foo.py::test_new"]
    )[2]
    assert "cp -a /workspace/." not in shell
    assert s.COPY_WORKTREE in shell
    assert '[ "$entry" = .git ]' in shell
    assert "parse_log_" not in shell
    _bash_n(shell)


def test_prepare_repo_disables_hooks(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A hook from the git template does not run on a commit in the task repo."""
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", os.devnull)
    monkeypatch.setenv("GIT_CONFIG_SYSTEM", os.devnull)
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")
    monkeypatch.setenv("GIT_AUTHOR_NAME", "Dev")
    monkeypatch.setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
    monkeypatch.setenv("GIT_COMMITTER_NAME", "Dev")
    monkeypatch.setenv("GIT_COMMITTER_EMAIL", "dev@example.com")

    cache = tmp_path / "cache"
    cached = cache / "example__repo"
    cached.mkdir(parents=True)
    _git(cached, "init", "-q")
    (cached / "README").write_text("hi\n")
    _git(cached, "add", "README")
    _git(cached, "commit", "-q", "-m", "init")
    base = _git(cached, "rev-parse", "HEAD")

    def fake_expanduser(path: str) -> str:
        if path == "~/.cache/aoa/swebench_repos":
            return str(cache)
        return os.path.expanduser(path)

    monkeypatch.setattr(s.os.path, "expanduser", fake_expanduser)

    template = tmp_path / "template"
    hooks = template / "hooks"
    hooks.mkdir(parents=True)
    marker = tmp_path / "hook-ran"
    hook = hooks / "post-commit"
    hook.write_text(f"#!/bin/sh\ntouch {shlex.quote(str(marker))}\n")
    hook.chmod(0o755)
    monkeypatch.setenv("GIT_TEMPLATE_DIR", str(template))

    work = tmp_path / "work"
    work.mkdir()
    dest = Path(s.prepare_repo(str(work), "example__repo-1", "example/repo", base))
    installed = dest / ".git" / "hooks" / "post-commit"
    assert installed.is_file()
    assert not marker.exists()

    configured = _git(dest, "config", "--local", "--get", "core.hooksPath")
    hooks_path = Path(configured)
    assert hooks_path.is_dir()
    assert list(hooks_path.iterdir()) == []
    assert os.path.abspath(configured) == os.path.abspath(dest / ".git" / "aoa-hooks")

    (dest / "README").write_text("changed\n")
    _git(dest, "add", "README")
    _git(dest, "commit", "-q", "-m", "change")
    assert _git(dest, "rev-parse", "HEAD") != base
    assert not marker.exists()


def test_runner_installs_swebench_for_the_adapter() -> None:
    """The precision runner must be able to import swebench 4.1.0."""
    script = Path(__file__).resolve().parent / "gate_precision_run.sh"
    text = script.read_text()
    assert (
        'uv run --with "swebench==4.1.0" python '
        '"$ROOT/scripts/swebench_to_tasks.py"'
    ) in text
