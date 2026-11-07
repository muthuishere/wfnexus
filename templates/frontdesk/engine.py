#!/usr/bin/env python3
"""frontdesk: a read-only snapshot of THIS engine, for the agent to answer from.

    engine.py   GETs $WFX_API and writes engine.json; always exit 0. An API that
                cannot be read is written as `error`, so the agent says it cannot
                see the engine instead of answering from nothing.

engine.json:
  workflows[]   {name, description, triggers} — what exists (templates left out)
  runs          counts by status over the recent runs
  recent[]      the newest runs {id, workflow, status, outcome, error, createdAt}
  failed[]      recent failed runs {id, workflow, step, error, createdAt}
  waiting[]     runs waiting on a person {id, workflow, step, status, since}
  question      the question: the chat message's text, or `question` on a manual run
  howto         where to look for how-to answers
  error         set when the API could not be read
It only GETs. It never starts, approves or changes anything.
"""
import json
import os
import urllib.request

RECENT = int(os.environ.get("ENGINE_RECENT_RUNS", "15"))


def api(path):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    req = urllib.request.Request(base + "/api" + path)
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.loads(r.read() or b"null")


def listing(v, key):
    return (v.get(key) or []) if isinstance(v, dict) else (v or [])


def short(r):
    return {"id": r.get("id"), "workflow": r.get("workflow"), "status": r.get("status"),
            "outcome": (r.get("outcome") or "")[:200], "error": (r.get("error") or "")[:300],
            "step": r.get("currentStep") or "", "createdAt": r.get("createdAt")}


def main():
    try:
        i = json.load(open("input.json"))
    except (OSError, ValueError):
        i = {}
    snap = {"question": ((i.get("message") or {}).get("text") or i.get("question") or "").strip(),
            "howto": "wfnexus docs: docs/ and the template READMEs; `wfx --help` for the CLI. "
                     "Say what you know; if it is not in engine.json, say so."}
    try:
        wfs = listing(api("/workflows"), "workflows")
        runs = sorted(listing(api("/runs"), "runs"), key=lambda r: r.get("createdAt") or "", reverse=True)
    except Exception as e:
        snap["error"] = f"cannot read the engine API: {e}"
        json.dump(snap, open("engine.json", "w"), indent=2)
        print(snap["error"])
        return
    counts = {}
    for r in runs:
        counts[r.get("status")] = counts.get(r.get("status"), 0) + 1
    snap.update({
        "workflows": [{"name": w.get("name"), "description": (w.get("description") or "")[:240],
                       "triggers": w.get("on") or w.get("triggers")} for w in wfs],
        "runs": counts,
        "recent": [short(r) for r in runs[:RECENT]],
        "failed": [short(r) for r in runs if r.get("status") == "failed"][:RECENT],
        "waiting": [{**short(r), "since": r.get("updatedAt")} for r in runs
                    if r.get("status") in ("awaiting_approval", "needs_input", "waiting")][:RECENT],
    })
    with open("engine.json", "w") as f:
        json.dump(snap, f, indent=2)
    print(f"{len(wfs)} workflows, {len(runs)} recent runs, {len(snap['failed'])} failed, {len(snap['waiting'])} waiting")


if __name__ == "__main__":
    main()
