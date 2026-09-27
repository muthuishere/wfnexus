#!/usr/bin/env python3
"""baseline.py — the facts before a single test is written.

1. A test-gap PR already open? Stop (exit 78): two racing PRs means a reviewer
   reads the same scenarios twice.
2. Branch off the run's base commit.
3. Run the suite the way the style step says the repo runs it, and COUNT —
   per-test ids and line coverage. This is the "before" the PR quotes.
4. A RED suite stops the run (exit 78, the report says why): a new test cannot
   be judged against a suite that already fails. With ALLOW_RED=true the red
   tests are frozen as `known_red` instead, and validate.py then demands that
   nothing ELSE goes red.
5. A command that runs no tests at all is the style step being wrong — exit 70.

Env: BRANCH, PR_PREFIX, ALLOW_RED, SCOPE (space-separated paths; narrows source_dirs). Reads style.json; writes before.json and
baseline.json.
"""
import json
import os
import subprocess
import sys

D = ".wfx-test-gap"
BRANCH = os.environ["BRANCH"]
PREFIX = os.environ.get("PR_PREFIX", "[test-gaps]")
ALLOW_RED = os.environ.get("ALLOW_RED", "false").lower() == "true"


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True)


SKIP = {".git", "node_modules", "vendor", "dist", "build", ".venv", "venv", "__pycache__", ".wfx-test-gap", "coverage"}


def resolve_source_dirs(style):
    """Hold the style step's source_dirs to the real tree.

    A name that is not a path (`streams` for `src/streams`) is found by basename;
    one inside a test dir (`shared` = tests/shared) is dropped; with nothing
    left, `src` or `lib` if present, else the repo root. The model describes;
    the filesystem decides.
    """
    tests = [t.strip("./") for t in style.get("test_dirs") or [] if t.strip("./")]
    in_tests = lambda p: any(p == t or p.startswith(t + "/") for t in tests)
    dirs = {}
    for dp, dn, fn in os.walk("."):
        dn[:] = [d for d in dn if d not in SKIP and not d.startswith(".")]
        for d in dn:
            dirs.setdefault(d, []).append(os.path.relpath(os.path.join(dp, d)))
    out, notes = [], []
    for d in style.get("source_dirs") or []:
        d = d.strip().rstrip("/") or "."
        if os.path.isdir(d) and not in_tests(d):
            out.append(d)
            continue
        found = dirs.get(os.path.basename(d), [])
        cands = [c for c in found if not in_tests(c)]
        if len(cands) == 1:
            out.append(cands[0])
            notes.append(f"source dir {d!r} resolved to {cands[0]!r}")
        elif found and not cands:
            notes.append(f"source dir {d!r} dropped: it is inside the test dirs")
        else:
            notes.append(f"source dir {d!r} dropped: " + ("ambiguous: " + ", ".join(cands) if cands else "not found"))
    if not out:
        out = [next((x for x in ("src", "lib") if os.path.isdir(x)), ".")]
        notes.append(f"no usable source dir from the style step; using {out[0]!r}")
    style["source_dirs"] = sorted(set(out))
    return notes


def main():
    base = git("rev-parse", "HEAD").stdout.strip()
    if not base:
        print("not a git checkout — test-gap-finder needs repo_path or repo_url", file=sys.stderr)
        sys.exit(70)
    style = json.load(open(f"{D}/style.json"))
    for n in resolve_source_dirs(style):
        print("style:", n)
    scope = [x for x in (os.environ.get("SCOPE") or "").split() if x.strip()]
    if scope:
        missing = [x for x in scope if not os.path.exists(x)]
        if missing:
            print(f"scope path(s) do not exist at {base[:10]}: {', '.join(missing)}", file=sys.stderr)
            sys.exit(70)
        # the whole suite still runs; the surface, coverage and scenarios are this portion only
        style["source_dirs"] = scope
        print(f"scope: {' '.join(scope)} (the whole suite runs; scenarios are found in this portion only)")
    json.dump(style, open(f"{D}/style.json", "w"), indent=1)

    r = subprocess.run(["gh", "pr", "list", "--state", "open", "--search", f"{PREFIX} in:title",
                        "--json", "number,title,url", "--limit", "20"], capture_output=True, text=True)
    if r.returncode == 0:
        open_prs = [p for p in json.loads(r.stdout or "[]") if p["title"].startswith(PREFIX)]
        if open_prs:
            print(f"a test-gap PR is already open: #{open_prs[0]['number']} {open_prs[0]['url']} — nothing to do until it merges")
            json.dump({"skipped": "open test-gap PR", "open_prs": open_prs}, open(f"{D}/baseline.json", "w"))
            sys.exit(78)
    else:
        print(f"note: could not list open PRs ({r.stderr.strip()[:160]}); continuing without the duplicate check")

    b = git("checkout", "-b", BRANCH)
    if b.returncode and git("checkout", BRANCH).returncode:
        print(f"could not create branch {BRANCH}: {b.stderr.strip()}", file=sys.stderr)
        sys.exit(70)

    print(f"base {base[:10]} · branch {BRANCH} · {style.get('framework')} · `{style.get('test_command')}`")
    s = subprocess.run([sys.executable, f"{D}/suite.py", "--out", f"{D}/before.json"], capture_output=True, text=True)
    print(s.stdout.rstrip())
    if s.returncode == 70:
        print(s.stderr[-1500:], file=sys.stderr)
        sys.exit(70)
    before = json.load(open(f"{D}/before.json"))
    if not before["tests"] and before["code"] != 0:
        print("the style step's test_command ran no tests and failed — it is not how this repo runs its suite:\n"
              + before.get("tail", "")[-1200:])
        sys.exit(70)
    red = sorted(t for t, st in before["tests"].items() if st in ("failed", "error"))
    if before["tests"] and all("::" not in t and st == "error" for t, st in before["tests"].items()):
        print("EVERY test module failed to import — that is the command, not a red suite. The style step's "
              "test_command/source_dirs do not match how this repo runs its tests:\n" + before.get("tail", "")[-1500:])
        sys.exit(70)
    json.dump({"base": base, "branch": BRANCH, "suite_ok": before["ok"], "totals": before["totals"],
               "coverage": before.get("coverage", {}).get("percent"), "known_red": red if ALLOW_RED else [],
               "gaps": before.get("gaps", [])}, open(f"{D}/baseline.json", "w"), indent=1)
    if before["ok"]:
        return
    print(f"\nBASELINE IS RED — {len(red)} test(s) fail before anything is written:")
    for t in red[:40]:
        print("  ", t)
    print(before.get("tail", "")[-1500:])
    if ALLOW_RED:
        print(f"\nallow_red_baseline=true: these {len(red)} are frozen as known-red. Every other test, and every "
              f"new one, must pass; a known-red test never counts as covering anything.")
        return
    print("\nStopping: a new test cannot be judged against a suite that already fails. Fix the suite, or re-run "
          "with allow_red_baseline=true to freeze these failures as known-red.")
    sys.exit(78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"baseline.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
