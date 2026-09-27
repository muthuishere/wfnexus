#!/usr/bin/env python3
"""prepare.py — a git worktree per fix_now finding, INSIDE the run's workspace.

The fix agent may not touch anything outside its workspace (the containment
guardrail), and a fix belongs in the repo that owns the broken file — often
not the run's own. So this run step, which the guardrail does not govern,
checks out a branch of the owning repo at ./fixes/<finding>, and the agent
works there with relative paths. The person's own checkout is never touched.

For an `untracked` finding the folder is copied in from the person's checkout,
minus anything that looks like a secret store, so the fix is "commit it".

Writes worktrees.json. Exit 0 = at least one worktree ready; 4 = none needed.
"""
import json
import os
import re
import shutil
import subprocess
import sys

SECRETISH = re.compile(r"(^|/)(\.env[^/]*|vault|secrets?|credentials?[^/]*|.*\.pem|.*\.key)$", re.I)


def sh(cmd, cwd=None):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    return r.returncode, (r.stdout + r.stderr).strip()


def base_of(repo):
    code, ref = sh(["git", "-C", repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"])
    return ref.split("/", 1)[1] if code == 0 and "/" in ref else "main"


def main():
    diag = (json.load(open("diagnosis.json")) or {}).get("diagnoses") or []
    findings = {f["id"]: f for f in json.load(open("findings.json"))["findings"]}
    ready = []
    for d in diag:
        if not d.get("fix_now"):
            continue
        f = findings.get(d["finding"], {})
        repo = d.get("repo") or f.get("repo")
        if not repo or not os.path.isdir(repo):
            print(f"SKIP {d['finding']}: no repo ({repo!r})")
            continue
        base = base_of(repo)
        sh(["git", "-C", repo, "fetch", "-q", "origin", base])
        wt = os.path.abspath(os.path.join("fixes", d["finding"]))
        branch = f"wfx/doctor-{d['finding']}"
        sh(["git", "-C", repo, "worktree", "remove", "--force", wt])
        sh(["git", "-C", repo, "branch", "-D", branch])
        code, msg = sh(["git", "-C", repo, "worktree", "add", "-q", "-b", branch, wt, f"origin/{base}"])
        if code != 0:
            print(f"SKIP {d['finding']}: worktree failed: {msg[-200:]}")
            continue
        copied = []
        if d.get("cause") == "untracked" and f.get("path") and os.path.isdir(f["path"]):
            rel = os.path.relpath(f["path"], repo)
            for dirpath, dirnames, filenames in os.walk(f["path"]):
                dirnames[:] = [x for x in dirnames if x != "__pycache__" and not SECRETISH.search(x)]
                for name in filenames:
                    src = os.path.join(dirpath, name)
                    r = os.path.relpath(src, repo)
                    if SECRETISH.search(r) or name.endswith((".pyc", ".jsonl")):
                        continue
                    dst = os.path.join(wt, r)
                    os.makedirs(os.path.dirname(dst), exist_ok=True)
                    shutil.copy2(src, dst)
                    copied.append(r)
            print(f"   copied {len(copied)} file(s) of {rel}")
        ready.append({"finding": d["finding"], "repo": repo, "worktree": wt,
                      "relative": os.path.relpath(wt), "branch": branch, "base": base, "copied": copied})
        print(f"READY {d['finding']}  {os.path.relpath(wt)}  ({branch} from origin/{base})")
    json.dump({"worktrees": ready}, open("worktrees.json", "w"), indent=1)
    return 0 if ready else 4


if __name__ == "__main__":
    sys.exit(main())
