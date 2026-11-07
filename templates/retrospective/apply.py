#!/usr/bin/env python3
"""retrospective (apply mode): make ONE approved proposal's change, through the
same API a person's `wfx` would use. Runs only after its approval gate.

    apply.py   reads input.json {proposal, fingerprint}; exit 0 when applied,
               2 when the change was refused (the run fails, loudly)

  add-step / edit-step / new-workflow  PUT /workflows/<name> {definition}
      — the normal save: a new version, or a git proposal on a git-backed project
  retire-workflow                      DELETE /workflows/<name>
  memory-fix                           POST /workflows/<name>/memory (add),
                                       PATCH or DELETE /memory/<id>,
                                       promote: PUT /state (workflow scope, key)
Then `applied-proposal <fingerprint>` is remembered in this workflow's memory.
"""
import json
import os
import sys
import urllib.parse
import urllib.request

SELF = os.environ.get("RETRO_WORKFLOW", "retrospective")


def api(path, method="GET", body=None):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + "/api" + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.loads(r.read() or b"null")


def main():
    try:
        i = json.load(open("input.json"))
        p, fp = i["proposal"], i["fingerprint"]
        kind, t, ch = p["kind"], p["target"], p.get("change") or {}
        wf = t["workflow"]
    except Exception as e:
        print(f"no proposal to apply: {e!r}")
        return 2
    q = urllib.parse.quote(wf, safe="")
    proj = ("?project=" + urllib.parse.quote(t["project"])) if t.get("project") else ""
    try:
        if kind in ("add-step", "edit-step", "new-workflow"):
            res = api(f"/workflows/{q}{proj}", "PUT", {"definition": ch["definition"]})
        elif kind == "retire-workflow":
            res = api(f"/workflows/{q}{proj}", "DELETE")
        elif kind == "memory-fix":
            m = ch["memory"]
            if m["op"] == "add":
                res = api(f"/workflows/{q}/memory{proj}", "POST", {"body": m["body"]})
            elif m["op"] == "promote":
                # A note becomes a page: one state key of this workflow, written
                # by this path only (one writer per page).
                res = api("/state", "PUT", {"scope": "workflow", "name": wf, "key": m["key"], "value": m["body"]})
            elif m["op"] == "edit":
                res = api(f"/memory/{urllib.parse.quote(m['id'], safe='')}", "PATCH", {"body": m["body"]})
            else:
                res = api(f"/memory/{urllib.parse.quote(m['id'], safe='')}", "DELETE")
        else:
            raise ValueError(f"unknown kind {kind!r}")
    except Exception as e:
        print(f"{kind} on {wf} was refused: {e}")
        return 2
    try:
        api(f"/workflows/{urllib.parse.quote(SELF, safe='')}/memory", "POST",
            {"body": f"applied-proposal {fp} {kind} {wf}/{t.get('step') or ''}"})
    except Exception as e:
        print(f"applied, but could not remember it: {e}")
    print(json.dumps({"applied": kind, "target": t, "fingerprint": fp, "result": res}, indent=2))
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(f"applied {kind} on {wf}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
