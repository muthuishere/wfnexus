#!/usr/bin/env python3
"""baseline.py — the facts before anything is touched.

1. Is a cleanup PR already open? Then stop: two cleanup PRs racing each other
   is how a reviewer ends up approving a removal twice (exit 78 = nothing to do).
2. Branch off the run's base commit (the worktree is detached; the branch is
   where every removal commit will land).
3. Build, vet and test the scope and COUNT — the "before" the PR will quote.
   A red baseline stops the run: a removal cannot be proven against a suite
   that was already failing (exit 70).

Env: SCOPE, BRANCH, PR_PREFIX. Writes .wfx-code-remover/baseline.json.
"""
import json
import os
import subprocess
import sys

D = ".wfx-code-remover"
SCOPE = os.environ.get("SCOPE") or "."
BRANCH = os.environ["BRANCH"]
PREFIX = os.environ.get("PR_PREFIX", "[dead-code]")


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True)


def main():
    base = git("rev-parse", "HEAD").stdout.strip()
    if not base:
        print("not a git checkout — code-remover needs repo_path or repo_url", file=sys.stderr)
        sys.exit(70)
    if not os.path.exists(SCOPE):
        print(f"scope {SCOPE!r} does not exist at {base[:10]}", file=sys.stderr)
        sys.exit(70)

    open_prs = []
    r = subprocess.run(["gh", "pr", "list", "--state", "open", "--search", f"{PREFIX} in:title",
                        "--json", "number,title,url", "--limit", "20"], capture_output=True, text=True)
    if r.returncode == 0:
        open_prs = [p for p in json.loads(r.stdout or "[]") if p["title"].startswith(PREFIX)]
    else:
        print(f"note: could not list open PRs ({r.stderr.strip()[:160]}); continuing without the duplicate check")
    if open_prs:
        print(f"a cleanup PR is already open: #{open_prs[0]['number']} {open_prs[0]['url']} — nothing to do until it merges")
        json.dump({"skipped": "open cleanup PR", "open_prs": open_prs}, open(f"{D}/baseline.json", "w"))
        sys.exit(78)

    b = git("checkout", "-b", BRANCH)
    if b.returncode:
        b = git("checkout", BRANCH)  # a resumed run: the branch already exists
        if b.returncode:
            print(f"could not create branch {BRANCH}: {b.stderr.strip()}", file=sys.stderr)
            sys.exit(70)

    print(f"base {base[:10]} · branch {BRANCH} · scope {SCOPE}")
    s = subprocess.run([sys.executable, f"{D}/suite.py", "--scope", SCOPE, "--out", f"{D}/before.json"],
                       capture_output=True, text=True)
    print(s.stdout.rstrip())
    if s.returncode == 70:
        print(s.stderr, file=sys.stderr)
        sys.exit(70)
    before = json.load(open(f"{D}/before.json"))
    json.dump({"base": base, "branch": BRANCH, "scope": SCOPE, "suite_ok": before["ok"],
               "totals": before["totals"], "gaps": before["gaps"]}, open(f"{D}/baseline.json", "w"), indent=1)
    if not before["ok"]:
        print("BASELINE IS RED — a removal cannot be proven against a suite that already fails. "
              "Fix the suite (or narrow `scope`) and run again.")
        for m in before["modules"]:
            for k in ("build", "vet", "test"):
                if m.get(k) and not m[k].get("ok", True):
                    print(f"  {m['dir']} {k}: {m[k].get('tail', '')[-600:]}")
        sys.exit(70)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"baseline.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
