#!/usr/bin/env python3
"""merge.py — merge each approved PR, then prove the workflow it fixed runs.

For each PR in prs.json: squash-merge it, remove its worktree, re-pull a
git-backed project so the platform loads the merged file, and dry-run the
workflow the finding was about. A merge whose re-check fails is reported
loudly — it is merged, so the next doctor run sees it as a new failure.

Exit 0 = every PR merged and re-checked. 1 = something did not.
"""
import json
import os
import subprocess
import sys


def sh(cmd, cwd=None, timeout=300):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    return r.returncode, (r.stdout + r.stderr).strip()


def main():
    if not os.path.exists("prs.json"):
        print("no pull requests to merge")
        return 0
    prs = json.load(open("prs.json")).get("prs") or []
    findings = {f["id"]: f for f in json.load(open("findings.json"))["findings"]}
    fixes = {f["finding"]: f for f in (json.load(open("fixes.json")).get("fixes") or [])} if os.path.exists("fixes.json") else {}
    merged, ok = [], True
    for pr in prs:
        code, msg = sh(["gh", "pr", "merge", pr["url"], "--squash", "--delete-branch"])
        if code != 0:
            print(f"NOT MERGED {pr['url']}: {msg[-300:]}")
            ok = False
            continue
        print(f"merged {pr['url']}")
        merged.append(pr["finding"])
        fix = fixes.get(pr["finding"], {})
        if fix.get("worktree") and os.path.isdir(fix["worktree"]):
            sh(["git", "-C", fix["repo"], "worktree", "remove", "--force", fix["worktree"]])
        f = findings.get(pr["finding"], {})
        if f.get("remote") and f.get("project"):
            code, msg = sh(["wfx", "project", "add", f["remote"], "--branch", fix.get("base") or "main", "--as", f["project"]])
            print(f"   re-pulled {f['project']}: {'ok' if code == 0 else msg[-200:]}")
        if f.get("workflow"):
            code, msg = sh(["wfx", "dryrun", f["workflow"]], timeout=120)
            print(f"   re-check {f['workflow']}: {'would run' if code == 0 else 'STILL BROKEN'}")
            if code != 0:
                print("   " + msg[-400:].replace("\n", "\n   "))
                ok = False
    json.dump({"merged": merged}, open("merged.json", "w"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
