#!/usr/bin/env python3
"""Grade a SWE-bench test log against PASS_TO_PASS ids.

This file executes inside old testbed images, so it stays on the Python 3.6
subset of the language: no walrus, no match, no builtin generics.

The three parsers and TestStatus are a vendored copy of swebench 4.1.0:

- swebench.harness.log_parsers.parse_log_django
- swebench.harness.log_parsers.parse_log_sympy
- swebench.harness.log_parsers.parse_log_pytest

from swebench/harness/log_parsers/python.py, and TestStatus from
swebench/harness/constants/__init__.py. Parser bodies are unchanged.
Signatures use typing forms that evaluate on Python 3.6, and the unused
upstream TestSpec argument is typed as object.

https://github.com/SWE-bench/SWE-bench/blob/v4.1.0/swebench/harness/log_parsers/python.py

Usage (source on stdin, which is how the Gate runs it):

    python - LOG REPO PASS_TO_PASS_JSON <<'EOF'
    ...this file...
    EOF

PASS_TO_PASS_JSON is a JSON list of test ids. An id missing from the log is
ignored. An id that ran must be PASSED or XFAIL. If none of the ids ran, the
command fails with "no PASS_TO_PASS test ran".
"""

import json
import re
import sys
from collections.abc import Sequence
from enum import Enum
from pathlib import Path


class TestStatus(Enum):
    """swebench 4.1.0 swebench.harness.constants.TestStatus."""

    FAILED = "FAILED"
    PASSED = "PASSED"
    SKIPPED = "SKIPPED"
    ERROR = "ERROR"
    XFAIL = "XFAIL"


def parse_log_pytest(log: str, test_spec: object) -> dict:
    """Parser for test logs generated with PyTest framework.

    Args:
        log (str): log content
    Returns:
        dict: test case to test status mapping
    """
    del test_spec
    test_status_map = {}
    for line in log.split("\n"):
        if any([line.startswith(x.value) for x in TestStatus]):  # noqa: C419
            # Additional parsing for FAILED status
            if line.startswith(TestStatus.FAILED.value):
                line = line.replace(" - ", " ")
            test_case = line.split()
            if len(test_case) <= 1:
                continue
            test_status_map[test_case[1]] = test_case[0]
    return test_status_map


def parse_log_django(log: str, test_spec: object) -> dict:
    """Parser for test logs generated with Django tester framework.

    Args:
        log (str): log content
    Returns:
        dict: test case to test status mapping
    """
    del test_spec
    test_status_map = {}
    lines = log.split("\n")

    prev_test = None
    for line in lines:
        line = line.strip()

        # This isn't ideal but the test output spans multiple lines
        if "--version is equivalent to version" in line:
            test_status_map["--version is equivalent to version"] = (
                TestStatus.PASSED.value
            )

        # Log it in case of error
        if " ... " in line:
            prev_test = line.split(" ... ")[0]

        pass_suffixes = (" ... ok", " ... OK", " ...  OK")
        for suffix in pass_suffixes:
            if line.endswith(suffix):
                # TODO: Temporary, exclusive fix for django__django-7188
                # The proper fix should involve somehow getting the test results to
                # print on a separate line, rather than the same line
                if line.strip().startswith(
                    "Applying sites.0002_alter_domain_unique...test_no_migrations"
                ):
                    line = line.split("...", 1)[-1].strip()
                test = line.rsplit(suffix, 1)[0]
                test_status_map[test] = TestStatus.PASSED.value
                break
        if " ... skipped" in line:
            test = line.split(" ... skipped")[0]
            test_status_map[test] = TestStatus.SKIPPED.value
        if line.endswith(" ... FAIL"):
            test = line.split(" ... FAIL")[0]
            test_status_map[test] = TestStatus.FAILED.value
        if line.startswith("FAIL:"):
            test = line.split()[1].strip()
            test_status_map[test] = TestStatus.FAILED.value
        if line.endswith(" ... ERROR"):
            test = line.split(" ... ERROR")[0]
            test_status_map[test] = TestStatus.ERROR.value
        if line.startswith("ERROR:"):
            test = line.split()[1].strip()
            test_status_map[test] = TestStatus.ERROR.value

        if line.lstrip().startswith("ok") and prev_test is not None:
            # It means the test passed, but there's some additional output (including new lines)
            # between "..." and "ok" message
            test = prev_test
            test_status_map[test] = TestStatus.PASSED.value

    # TODO: This is very brittle, we should do better
    # There's a bug in the django logger, such that sometimes a test output near the end gets
    # interrupted by a particular long multiline print statement.
    # We have observed this in one of 3 forms:
    # - "{test_name} ... Testing against Django installed in {*} silenced.\nok"
    # - "{test_name} ... Internal Server Error: \/(.*)\/\nok"
    # - "{test_name} ... System check identified no issues (0 silenced).\nok"
    patterns = [
        r"^(.*?)\s\.\.\.\sTesting\ against\ Django\ installed\ in\ ((?s:.*?))\ silenced\)\.\nok$",
        r"^(.*?)\s\.\.\.\sInternal\ Server\ Error:\ \/(.*)\/\nok$",
        r"^(.*?)\s\.\.\.\sSystem check identified no issues \(0 silenced\)\nok$",
    ]
    for pattern in patterns:
        for match in re.finditer(pattern, log, re.MULTILINE):
            test_name = match.group(1)
            test_status_map[test_name] = TestStatus.PASSED.value
    return test_status_map


def parse_log_sympy(log: str, test_spec: object) -> dict:
    """Parser for test logs generated with Sympy framework.

    Args:
        log (str): log content
    Returns:
        dict: test case to test status mapping
    """
    del test_spec
    test_status_map = {}
    pattern = r"(_*) (.*)\.py:(.*) (_*)"
    matches = re.findall(pattern, log)
    for match in matches:
        test_case = f"{match[1]}.py:{match[2]}"
        test_status_map[test_case] = TestStatus.FAILED.value
    for line in log.split("\n"):
        line = line.strip()
        if line.startswith("test_"):
            if line.endswith(" E"):
                test = line.split()[0]
                test_status_map[test] = TestStatus.ERROR.value
            if line.endswith(" F"):
                test = line.split()[0]
                test_status_map[test] = TestStatus.FAILED.value
            if line.endswith(" ok"):
                test = line.split()[0]
                test_status_map[test] = TestStatus.PASSED.value
    return test_status_map


# Repos whose swebench 4.1.0 parser is one of the three functions above.
# Variants (pytest v2, pytest-options) are intentionally absent.
_PARSERS = {
    "django/django": parse_log_django,
    "sympy/sympy": parse_log_sympy,
    "pytest-dev/pytest": parse_log_pytest,
    "marshmallow-code/marshmallow": parse_log_pytest,
    "pallets/flask": parse_log_pytest,
    "pvlib/pvlib-python": parse_log_pytest,
    "pydata/xarray": parse_log_pytest,
    "pylint-dev/astroid": parse_log_pytest,
    "pyvista/pyvista": parse_log_pytest,
    "sqlfluff/sqlfluff": parse_log_pytest,
}

_PASSING = (TestStatus.PASSED.value, TestStatus.XFAIL.value)


def grade(log: str, repo: str, test_ids: Sequence) -> int:
    """Return 0 when every PASS_TO_PASS id that ran is PASSED or XFAIL.

    Prints "no PASS_TO_PASS test ran" when none of the ids appear. Prints
    each id whose status is anything else, with that status. Ids missing
    from the log are ignored.
    """
    parser = _PARSERS.get(repo)
    if parser is None:
        print(f"no log parser for {repo}")
        return 1
    status_map = parser(log, None)
    saw = False
    bad = []
    for test_id in test_ids:
        if test_id not in status_map:
            continue
        saw = True
        status = status_map[test_id]
        if status not in _PASSING:
            bad.append((test_id, status))
    if not saw:
        print("no PASS_TO_PASS test ran")
        return 1
    if bad:
        for test_id, status in bad:
            print(f"{test_id} {status}")
        return 1
    return 0


def main(argv: Sequence) -> int:
    """Grade argv: LOG, REPO, PASS_TO_PASS JSON list."""
    if len(argv) != 4:
        print("usage: python swebench_gate_grade.py LOG REPO PASS_TO_PASS_JSON")
        return 2
    log_path, repo, raw_ids = argv[1], argv[2], argv[3]
    try:
        with Path(log_path).open("r", encoding="utf-8", errors="replace") as handle:
            log = handle.read()
    except OSError as exc:
        print(f"cannot read log: {exc}")
        return 1
    try:
        parsed = json.loads(raw_ids)
    except ValueError as exc:
        print(f"PASS_TO_PASS is not JSON: {exc}")
        return 1
    if not isinstance(parsed, list) or not all(
        isinstance(item, str) for item in parsed
    ):
        print("PASS_TO_PASS must be a JSON list of strings")
        return 1
    return grade(log, repo, parsed)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
