#!/usr/bin/env python3
"""collect.py — what is broken across every project's workflows? Read-only.

Four kinds of finding, all from the platform's own API and the source trees:

  run_failed    a run that ended `failed` since the last look (grouped: the same
                workflow failing the same way is ONE finding, with a count)
  load_problem  a workflow file the platform refused to load
  incomplete    a workflow/template folder with no workflow.yaml
  untracked     a workflow folder that is not in git — nobody can review it,
                a clone never sees it, and a worktree run cannot find it

Rules before models: a missing secret is classified here (needs_secret), not
by an agent, because the fix is a person running `wfx env set`, never code.
Findings already handed to a PR (workflow state `handled`) are not repeated.

Exit 0 = nothing to do. Exit 3 = findings (findings.json and stdout).
Exit 5 = another doctor run is active, so this one does nothing.
Env: WFX_API (http://127.0.0.1:8090), SINCE_HOURS (24, first look only).
"""
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.request

API = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
SELF = "workflow-doctor"


def get(path):
    with urllib.request.urlopen(API + path, timeout=20) as r:
        return json.load(r)


def state(key):
    r = subprocess.run(["wfx", "state", "get", "--workflow", key], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else ""


def fp(*parts):
    return hashlib.sha1("|".join(parts).encode()).hexdigest()[:12]


def signature(err):
    """The failure without what changes run to run, so repeats group."""
    s = re.sub(r"[0-9a-f]{8}-[0-9a-f-]{27,}", "<id>", err or "")
    s = re.sub(r"\d{4}-\d\d-\d\dT[\d:.]+Z?", "<time>", s)
    return s[:300]


def git_root(path):
    r = subprocess.run(["git", "-C", path, "rev-parse", "--show-toplevel"], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else ""


def untracked(root, rel):
    r = subprocess.run(["git", "-C", root, "ls-files", "--", rel], capture_output=True, text=True)
    return r.returncode == 0 and not r.stdout.strip()


def gather_evidence(f):
    """Copy what the diagnosis needs INTO the workspace: the agent may not read
    outside it. The workflow's folder (secrets left out) and the last failing
    run's own record."""
    import shutil
    ev = os.path.join("evidence", f["id"])
    os.makedirs(ev, exist_ok=True)
    folder = f.get("path") or (os.path.join(f["source_dir"], f["workflow"].split("/")[-1])
                               if f.get("source_dir") and f.get("workflow") else "")
    if folder and os.path.isdir(folder):
        secretish = re.compile(r"(^|/)(\.env[^/]*|vault|secrets?|credentials?[^/]*|.*\.pem|.*\.key)$", re.I)
        for dirpath, dirs, files in os.walk(folder):
            dirs[:] = [d for d in dirs if d != "__pycache__" and not secretish.search(d)]
            for name in files:
                src = os.path.join(dirpath, name)
                rel = os.path.relpath(src, folder)
                if secretish.search(rel) or name.endswith((".pyc", ".jsonl")) or os.path.getsize(src) > 512_000:
                    continue
                dst = os.path.join(ev, "folder", rel)
                os.makedirs(os.path.dirname(dst), exist_ok=True)
                shutil.copy2(src, dst)
        f["evidence_folder"] = os.path.join(ev, "folder")
    run = next((r for r in reversed(f.get("run_ids") or []) if r), None)
    if run:
        for name, cmd in (("run.txt", ["wfx", "show", run]), ("run.log", ["wfx", "logs", run])):
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
            open(os.path.join(ev, name), "w").write(r.stdout[-200_000:])
        f["evidence_run"] = os.path.join(ev, "run.log")


def main():
    now = dt.datetime.now(dt.timezone.utc)
    since = state("last_checked")
    if not since:
        since = (now - dt.timedelta(hours=float(os.environ.get("SINCE_HOURS", "24")))).isoformat()
    handled = set(filter(None, state("handled").split(",")))
    try:
        carried = json.loads(state("open") or "[]")  # unresolved findings from earlier looks
    except ValueError:
        carried = []

    try:
        projects = get("/api/projects")
        projects = projects.get("projects", projects) if isinstance(projects, dict) else projects
        runs = get("/api/runs?status=failed&limit=200")
        runs = runs.get("runs", runs) if isinstance(runs, dict) else runs
    except Exception as e:  # a doctor that cannot see is itself a finding, never "healthy"
        print(f"cannot read the platform API at {API}: {e}")
        return 2

    # One doctor at a time: two would check out the same branches and take
    # each other's worktrees away (seen: the hourly run did exactly that).
    me = os.environ.get("WFX_RUN_ID", "")
    try:
        live = get("/api/runs?workflow=" + SELF + "&status=running,awaiting_approval,needs_input&limit=20")
        live = live.get("runs", live) if isinstance(live, dict) else live
    except Exception:
        live = []
    others = [r["id"] for r in live if r.get("id") != me]
    if others:
        print(f"another workflow-doctor run is active ({others[0]}); this one stands down")
        return 5

    by_project = {p["name"]: p for p in projects}
    findings = {}

    def add(kind, **f):
        key = fp(kind, f.get("workflow", ""), f.get("path", ""), f.get("signature", ""))
        if key in handled:
            return
        if key in findings:
            findings[key]["count"] += 1
            findings[key]["run_ids"].append(f.get("run_id"))
            return
        f.update(id=key, kind=kind, count=1, run_ids=[f.pop("run_id", None)])
        findings[key] = f

    for r in runs:
        if r.get("createdAt", "") < since or r.get("workflow", "").split("/")[-1] == SELF:
            continue
        p = by_project.get(r.get("project"), {})
        err = r.get("error", "")
        m = re.search(r"env: ([A-Z0-9_, ]+) (?:is|are) not set", err)
        transient = bool(re.search(r"interrupted by a server restart|context deadline exceeded|rate.?limit|429", err, re.I))
        add("transient" if transient else "run_failed", workflow=r["workflow"], project=r.get("project"), step=r.get("currentStep"),
            error=err[:600], signature=signature(err), run_id=r["id"],
            source_dir=p.get("dir", ""), repo=git_root(p.get("dir", "")) if p.get("dir") else "",
            remote=p.get("url") or "",
            needs_secret=[s.strip() for s in m.group(1).split(",")] if m else [])

    for p in projects:
        for prob in p.get("problems") or []:
            reason = prob.get("reason", "")
            if "is already used by source" in reason:
                continue  # a reported collision, both names reachable — not breakage
            add("load_problem", project=p["name"], path=prob.get("location", ""), error=reason[:600],
                signature=signature(reason), repo=git_root(p.get("dir", "")), remote=p.get("url") or "")

    # the platform's own templates are a source too; the API lists workflows, not template folders
    roots = [(p["name"], p.get("dir", "")) for p in projects if p.get("dir")]
    bfp = git_root(by_project.get("local", {}).get("dir", "."))
    if bfp and os.path.isdir(os.path.join(bfp, "templates")):
        roots.append(("templates", os.path.join(bfp, "templates")))
    for name, d in roots:
        if not os.path.isdir(d):
            continue
        root = git_root(d)
        if not root:
            add("untracked", project=name, path=d, repo="", error="project directory is not a git repository",
                signature="not-a-repo")
        for entry in sorted(os.listdir(d)):
            full = os.path.join(d, entry)
            if not os.path.isdir(full) or entry.startswith("."):
                continue
            if not os.path.exists(os.path.join(full, "workflow.yaml")):
                add("incomplete", project=name, path=full, repo=root,
                    error=f"folder has no workflow.yaml (holds: {', '.join(sorted(os.listdir(full)))[:200]})",
                    signature="incomplete")
            elif root and untracked(root, os.path.relpath(full, root)):
                add("untracked", project=name, path=full, repo=root,
                    error="workflow folder is not in git", signature="untracked")

    for f in carried:
        if f.get("id") and f["id"] not in findings and f["id"] not in handled and f.get("kind") in ("run_failed", "transient"):
            findings[f["id"]] = f  # still broken until a fix merges; a new run of it would re-add it anyway
    for f in findings.values():
        gather_evidence(f)
    out = {"checked_at": now.isoformat(), "since": since, "findings": list(findings.values())}
    json.dump(out, open("findings.json", "w"), indent=1)
    for f in out["findings"]:
        tag = "needs_secret " + ",".join(f["needs_secret"]) if f.get("needs_secret") else f["kind"]
        print(f"{f['id']}  {tag:<28} x{f['count']}  {f.get('workflow') or f.get('path')}  — {f['error'][:140]}")
    print(f"{len(out['findings'])} finding(s) since {since}")
    return 3 if out["findings"] else 0


if __name__ == "__main__":
    sys.exit(main())
