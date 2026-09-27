#!/usr/bin/env python3
"""verify.py — prove every fix in fixes.json; a claim is not a fix.

Per fix, in its worktree:
  - the branch has commits past its base and nothing uncommitted
  - every changed workflow.yaml passes `wfx validate`
  - Go changed → `go test ./...` in the module that changed
  - no changed file holds a live secret (`sec seal --check` when sec exists)

Exit 0 = fixes exist and all are proven. 1 = at least one failed. 4 = nothing
to verify (no fix step ran, or it fixed nothing). Detail in verify.json.
"""
import json
import os
import shutil
import subprocess
import sys


def sh(cmd, cwd, timeout=900):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    return r.returncode, (r.stdout + r.stderr).strip()


def go_module(worktree, path):
    d = os.path.dirname(os.path.join(worktree, path))
    while d.startswith(worktree):
        if os.path.exists(os.path.join(d, "go.mod")):
            return d
        d = os.path.dirname(d)
    return ""


def check(fix):
    wt, base = fix["worktree"], fix.get("base") or "main"
    out = {"finding": fix["finding"], "checks": [], "ok": True}

    def record(name, ok, detail=""):
        out["checks"].append({"check": name, "ok": ok, "detail": detail[-800:]})
        out["ok"] = out["ok"] and ok

    if not os.path.isdir(wt):
        record("worktree exists", False, wt)
        return out
    code, ahead = sh(["git", "rev-list", "--count", f"origin/{base}..HEAD"], wt)
    record("committed on the branch", code == 0 and ahead.isdigit() and int(ahead) > 0, f"{ahead} commit(s) past origin/{base}")
    _, dirty = sh(["git", "status", "--porcelain"], wt)
    record("nothing left uncommitted", not dirty, dirty)
    _, changed = sh(["git", "diff", "--name-only", f"origin/{base}...HEAD"], wt)
    files = [f for f in changed.splitlines() if f]
    if not files:
        record("the branch changes something", False)

    for f in files:
        if f.endswith("workflow.yaml") and os.path.exists(os.path.join(wt, f)):
            code, msg = sh(["wfx", "validate", f], wt, 120)
            record(f"wfx validate {f}", code == 0, msg)
    for mod in sorted({go_module(wt, f) for f in files if f.endswith(".go")} - {""}):
        code, msg = sh(["go", "test", "./...", "-count=1"], mod)
        record(f"go test {os.path.relpath(mod, wt)}", code == 0, msg)
    if shutil.which("sec"):
        for f in files:
            if os.path.exists(os.path.join(wt, f)):
                code, msg = sh(["sec", "seal", f, "--check"], wt, 60)
                record(f"no secret in {f}", code == 0, msg)
    return out


def main():
    if not os.path.exists("fixes.json"):
        print("no fixes to verify")
        return 4
    fixes = json.load(open("fixes.json")).get("fixes") or []
    if not fixes:
        print("the fix step fixed nothing")
        return 4
    results = [check(f) for f in fixes]
    json.dump({"results": results}, open("verify.json", "w"), indent=1)
    for r in results:
        print(("PROVEN " if r["ok"] else "FAILED ") + r["finding"])
        for c in r["checks"]:
            print(f"   {'ok  ' if c['ok'] else 'FAIL'} {c['check']}" + ("" if c["ok"] else f" — {c['detail'][-300:]}"))
    return 0 if all(r["ok"] for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
