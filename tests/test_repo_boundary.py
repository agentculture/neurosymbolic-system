"""t16 acceptance criterion 2: this branch touches nothing outside the repo.

``neurosymbolic-system`` is the only repository this agent's work is allowed
to change (see ``CLAUDE.md``, "Working across the sibling repos — read them,
never write them"). This test is the mechanical backstop for that rule: it
diffs the current branch against ``main`` (falling back to ``origin/main`` if
``main`` is not a local ref, e.g. a shallow clone or a fresh worktree that
never fetched it) and asserts every changed path is a plain relative path
under the repository root — no absolute path, and no ``..`` component that
could climb out of it. It also asserts ``git status --porcelain`` at the repo
root reports only such paths, which is trivially true for any git worktree
but is asserted anyway so a future change to how this test locates "the repo
root" is caught immediately rather than silently checking the wrong tree.
"""

from __future__ import annotations

import subprocess  # nosec B404 - argv-list git invocations only, no shell
from pathlib import Path


def _run_git(*args: str, cwd: Path) -> str:
    result = subprocess.run(  # nosec B603 - argv list, shell=False
        ["git", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        shell=False,
        check=True,
    )
    return result.stdout


def _repo_root() -> Path:
    here = Path(__file__).resolve().parent
    top = _run_git("rev-parse", "--show-toplevel", cwd=here).strip()
    return Path(top)


def _ref_exists(ref: str, *, cwd: Path) -> bool:
    result = subprocess.run(  # nosec B603 - argv list, shell=False
        ["git", "rev-parse", "--verify", "--quiet", ref],
        cwd=cwd,
        capture_output=True,
        text=True,
        shell=False,
        check=False,
    )
    return result.returncode == 0


def _base_ref(repo_root: Path) -> str:
    """The ref this branch's diff is measured against.

    ``main`` first; ``origin/main`` when ``main`` names no local ref (a fresh
    worktree that only fetched a branch, or a shallow clone).
    """
    if _ref_exists("main", cwd=repo_root):
        return "main"
    if _ref_exists("origin/main", cwd=repo_root):
        return "origin/main"
    raise RuntimeError(
        "neither 'main' nor 'origin/main' resolves in this checkout; "
        "cannot compute the branch's diff against a base ref"
    )


def _changed_paths(repo_root: Path) -> list[str]:
    base = _base_ref(repo_root)
    output = _run_git("diff", "--name-only", f"{base}...HEAD", cwd=repo_root)
    return [line for line in output.splitlines() if line]


def _assert_relative_and_contained(paths: list[str], *, label: str) -> None:
    for path in paths:
        assert not path.startswith("/"), f"{label} path {path!r} is absolute, not relative"
        parts = Path(path).parts
        assert ".." not in parts, f"{label} path {path!r} climbs out of the repo root via '..'"


def test_branch_diff_touches_no_path_outside_the_repo() -> None:
    """Every path changed on this branch (vs. main) stays inside the repo."""
    repo_root = _repo_root()
    changed = _changed_paths(repo_root)
    assert changed, "expected at least one changed path on this branch"
    _assert_relative_and_contained(changed, label="diff")


def test_working_tree_status_touches_no_path_outside_the_repo() -> None:
    """`git status --porcelain` at the repo root names only in-repo paths.

    This is trivially true for any git invocation (git reports paths relative
    to the repo root by construction), but it is asserted anyway: if this
    test's own notion of "the repo root" is ever wrong (e.g. it drifts to a
    parent directory that holds sibling checkouts), THIS assertion is what
    would catch it, since git would then report paths reaching into a sibling.
    """
    repo_root = _repo_root()
    output = _run_git("status", "--porcelain", cwd=repo_root)
    paths: list[str] = []
    for line in output.splitlines():
        if not line:
            continue
        # Porcelain format: "XY PATH" or "XY ORIG -> PATH" for a rename.
        entry = line[3:]
        path = entry.split(" -> ", 1)[-1]
        paths.append(path)
    _assert_relative_and_contained(paths, label="status")
