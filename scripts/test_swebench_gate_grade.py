"""Per-test PASS_TO_PASS grading for django, sympy, and pytest logs."""

from __future__ import annotations

import ast
import json
import subprocess
import sys
from pathlib import Path

import pytest
import swebench_gate_grade as g

_DJ = "django/django"
_SY = "sympy/sympy"
_PY = "pytest-dev/pytest"

_D_ABS = "test_abs (app.tests.Tests)"
_D_KEEP = "test_keep (app.tests.Tests)"
_D_BAD = "test_bad (app.tests.Tests)"
_D_ERR = "test_err (app.tests.Tests)"
_D_OTHER = "test_other (app.tests.Tests)"
_D_NEW = "test_new (app.tests.Tests)"

_P_ABS = "tests/test_foo.py::test_abs"
_P_KEEP = "tests/test_foo.py::test_keep"
_P_BAD = "tests/test_foo.py::test_bad"
_P_ERR = "tests/test_foo.py::test_err"
_P_OTHER = "tests/test_foo.py::test_other"
_P_NEW = "tests/test_foo.py::test_new"
_P_XFAIL = "tests/test_foo.py::test_known"


@pytest.mark.parametrize(
    ("repo", "log", "ids", "code", "needles"),
    [
        pytest.param(
            _DJ,
            f"{_D_ABS} ... ok\n{_D_KEEP} ... ok\n",
            [_D_ABS, _D_KEEP],
            0,
            [],
            id="django-all-pass",
        ),
        pytest.param(
            _DJ,
            f"{_D_ABS} ... ok\n{_D_BAD} ... FAIL\n",
            [_D_ABS, _D_BAD],
            1,
            [_D_BAD, "FAILED"],
            id="django-one-failed",
        ),
        pytest.param(
            _DJ,
            f"{_D_ABS} ... ok\n{_D_ERR} ... ERROR\n",
            [_D_ABS, _D_ERR],
            1,
            [_D_ERR, "ERROR"],
            id="django-one-error",
        ),
        pytest.param(
            _DJ,
            f"{_D_OTHER} ... ok\n",
            [_D_ABS],
            1,
            ["no PASS_TO_PASS test ran"],
            id="django-none-present",
        ),
        pytest.param(
            _DJ,
            f"{_D_ABS} ... ok\n",
            [_D_ABS, _D_NEW],
            0,
            [],
            id="django-missing-id-ignored",
        ),
        pytest.param(
            _DJ,
            f"{_D_ABS} ... ok\n{_D_ERR} ... ERROR\n",
            [_D_ABS],
            0,
            [],
            id="django-unrelated-error-ignored",
        ),
        pytest.param(
            _SY,
            "test_abs ok\ntest_keep ok\n",
            ["test_abs", "test_keep"],
            0,
            [],
            id="sympy-all-pass",
        ),
        pytest.param(
            _SY,
            "test_abs ok\ntest_bad F\n",
            ["test_abs", "test_bad"],
            1,
            ["test_bad", "FAILED"],
            id="sympy-one-failed",
        ),
        pytest.param(
            _SY,
            "test_abs ok\ntest_err E\n",
            ["test_abs", "test_err"],
            1,
            ["test_err", "ERROR"],
            id="sympy-one-error",
        ),
        pytest.param(
            _SY,
            "test_other ok\n",
            ["test_abs"],
            1,
            ["no PASS_TO_PASS test ran"],
            id="sympy-none-present",
        ),
        pytest.param(
            _SY,
            "test_abs ok\n",
            ["test_abs", "test_new"],
            0,
            [],
            id="sympy-missing-id-ignored",
        ),
        pytest.param(
            _SY,
            "test_abs ok\ntest_err E\n",
            ["test_abs"],
            0,
            [],
            id="sympy-unrelated-exception-ignored",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_ABS}\nPASSED {_P_KEEP}\n",
            [_P_ABS, _P_KEEP],
            0,
            [],
            id="pytest-all-pass",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_ABS}\nFAILED {_P_BAD} - assert False\n",
            [_P_ABS, _P_BAD],
            1,
            [_P_BAD, "FAILED"],
            id="pytest-one-failed",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_ABS}\nERROR {_P_ERR}\n",
            [_P_ABS, _P_ERR],
            1,
            [_P_ERR, "ERROR"],
            id="pytest-one-error",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_OTHER}\n",
            [_P_ABS],
            1,
            ["no PASS_TO_PASS test ran"],
            id="pytest-none-present",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_ABS}\n",
            [_P_ABS, _P_NEW],
            0,
            [],
            id="pytest-missing-id-ignored",
        ),
        pytest.param(
            _PY,
            f"PASSED {_P_ABS}\nXFAIL {_P_XFAIL}\n",
            [_P_ABS, _P_XFAIL],
            0,
            [],
            id="pytest-xfail-passes",
        ),
    ],
)
def test_grade_fixture_log(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
    repo: str,
    log: str,
    ids: list[str],
    code: int,
    needles: list[str],
) -> None:
    path = tmp_path / "log.txt"
    path.write_text(log)
    got = g.main(["swebench_gate_grade.py", str(path), repo, json.dumps(ids)])
    out = capsys.readouterr().out
    assert got == code
    if code == 0:
        assert out == ""
        return
    for needle in needles:
        assert needle in out
    if "no PASS_TO_PASS test ran" not in needles:
        assert "no PASS_TO_PASS test ran" not in out


def test_grader_runs_as_python_stdin(tmp_path: Path) -> None:
    """The Gate invokes the grader as `python - LOG REPO JSON`."""
    log = tmp_path / "log.txt"
    log.write_text("test_abs ok\ntest_err E\n")
    source = Path(g.__file__).read_text(encoding="utf-8")
    proc = subprocess.run(
        [sys.executable, "-", str(log), "sympy/sympy", json.dumps(["test_abs"])],
        input=source,
        text=True,
        capture_output=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stdout + proc.stderr
    assert proc.stdout == ""


def test_grader_source_stays_on_python36() -> None:
    """Old testbed Pythons reject walrus, match, and builtin generics."""
    text = Path(g.__file__).read_text(encoding="utf-8")
    tree = ast.parse(text)
    for node in ast.walk(tree):
        assert not isinstance(node, ast.NamedExpr)
        assert not isinstance(node, ast.Match)
    for token in ("list[", "dict[", "tuple[", "set[", "\nEOF\n"):
        assert token not in text
