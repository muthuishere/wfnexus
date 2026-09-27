#!/usr/bin/env python3
"""dryrun-draft.py — dry-run a workflow that is NOT installed yet.

`wfx dryrun <name>` only knows workflows the server has loaded, and loading one
means `wfx apply`, which installs it. A recipe draft must be checked BEFORE the
person says yes, so this posts the draft to the server's draft endpoint
(POST /api/dryrun) instead: same checks, nothing written, nothing run.

Usage:
  dryrun-draft.py <dir-with-workflow.yaml | file.yaml> [-i key=value ...]

The sidecar files beside a directory-form workflow (questions.yaml, collect.sh…)
are sent with it, so a step that names one is checked against what exists.
Exit 0 when the dry run has no fatal problem, 1 otherwise.
"""
import base64
import json
import os
import sys
import urllib.request

import yaml

API = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")


def load(path):
    if os.path.isdir(path):
        root, wf = path, os.path.join(path, "workflow.yaml")
    else:
        root, wf = None, path
    with open(wf) as f:
        d = yaml.safe_load(f)
    # YAML 1.1 reads a bare `on:` key as the boolean True.
    if True in d:
        d["on"] = d.pop(True)
    on = d.get("on") or {}
    if isinstance(on, dict):
        trig = {}
        if "workflow_dispatch" in on:
            trig["dispatch"] = True
        if on.get("schedule"):
            trig["schedule"] = on["schedule"]
        if on.get("repository_dispatch") is not None:
            trig["repository_dispatch"] = on["repository_dispatch"] or {}
        d["on"] = trig
    files = []
    if root:
        for dirpath, _, names in os.walk(root):
            for n in names:
                p = os.path.join(dirpath, n)
                rel = os.path.relpath(p, root)
                if rel == "workflow.yaml":
                    continue
                with open(p, "rb") as f:
                    body = f.read()
                files.append({"path": rel, "size": len(body),
                              "mode": os.stat(p).st_mode & 0o777,
                              "body": base64.b64encode(body).decode()})
    if files:
        d["files"] = files
    return d


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    d = load(sys.argv[1])
    inp, args = {}, sys.argv[2:]
    while args:
        a = args.pop(0)
        if a == "-i" and args:
            k, _, v = args.pop(0).partition("=")
            inp[k] = v
    req = urllib.request.Request(API + "/api/dryrun", method="POST",
                                 data=json.dumps({"definition": d, "input": inp}).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        res = json.load(r)
    print(f"{res.get('workflow')} — {res.get('shape')}, {len(res.get('steps') or [])} steps")
    for i, wave in enumerate(res.get("waves") or []):
        print(f"  wave {i + 1}: {', '.join(wave)}")
    for s in res.get("steps") or []:
        where = s.get("shell") if s.get("kind") == "run" else (s.get("provider") or s.get("model") or "default")
        print(f"  {s.get('id'):<18} {s.get('kind'):<7} {where}")
    cost = res.get("cost") or {}
    print(f"ceiling: {cost.get('maxTurns', 0)} model turns across {cost.get('agentSteps', 0)} agent step(s); "
          f"{cost.get('freeSteps', 0)} step(s) call no model")
    fatal = False
    for p in res.get("problems") or []:
        fatal = fatal or p.get("fatal")
        print(f"  {'✗' if p.get('fatal') else '!'} {p.get('step') or '-'} {p.get('field') or ''}: {p.get('message')}")
    print("OK — would run" if res.get("ok") and not fatal else "NOT OK — fix the problems above")
    sys.exit(0 if res.get("ok") and not fatal else 1)


if __name__ == "__main__":
    main()
