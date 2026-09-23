"""--gate=repo for repos whose PASS_TO_PASS ids are not pytest node ids.

Pytest node ids (they contain ``::``) keep the historical file-level pytest
gate. Django and sympy ids do not, so the gate runs the test files named in
the held-out test patch, with the repo's own test command.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest
import swebench_to_tasks as s

_PREFIX = (
    "cp -a /workspace/. /testbed/ && "
    "source /opt/miniconda3/bin/activate && conda activate testbed && "
    "cd /testbed && "
)

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
    assert gates == [
        "gate = "
        + s.toml_cmd_list(
            [s.conda_shell(f"{DJANGO_CMD} migrations.test_operations")]
        )
    ]
    script = gates[0]
    assert script.count("/bin/bash") == 1
    assert f"{_PREFIX}{DJANGO_CMD} migrations.test_operations" in script
    assert "x.txt" not in script
    assert "decoy_only_in_body" not in script
    assert "python -m pytest" not in script
    assert "test_held_out" not in script
    assert "test_abs" not in script


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
    assert f"{_PREFIX}{SYMPY_CMD} sympy/matrices/tests/test_commonmatrix.py" in gates[0]
    assert "python -m pytest" not in gates[0]


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
    assert gates == [
        "gate = "
        + s.toml_cmd_list(
            [s.conda_shell(f"{DJANGO_CMD} migrations.test_operations")]
        )
    ]
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
    assert expected[2] == (
        f"{_PREFIX}python -m pytest -q tests/test_foo.py "
        "--deselect tests/test_foo.py::test_new"
    )
    assert "SHOULD_NOT_RUN" not in text


def test_runner_installs_swebench_for_the_adapter() -> None:
    """The precision runner must be able to import swebench 4.1.0."""
    script = Path(__file__).resolve().parent / "gate_precision_run.sh"
    text = script.read_text()
    assert (
        'uv run --with "swebench==4.1.0" python '
        '"$ROOT/scripts/swebench_to_tasks.py"'
    ) in text
