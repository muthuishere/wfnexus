#!/usr/bin/env python3
"""retrospective: gather what happened, from $WFX_API only. Read-only.

    collect.py   reads input.json, writes facts.json; exit 0, or 2 when the API
                 cannot be read (a retrospective that cannot see never says "all fine")

facts.json:
  window          {since, until}
  focus           {workflow, runId, question} when a caller (frontdesk) asked about one
  runs            per workflow: total, by status, retries, approval wait, tokens, cost
  failures[]      {workflow, runId, stepId, error, attempts, at}: the citable evidence
  retries[]       steps that needed more than one attempt
  waits[]         approvals/questions and how long a person took (or is taking)
  previous        the last retrospective's findings, each marked "recurred" (its
                  workflow failed again after it) or "no new failure"
  steps           {runId: [stepId]} for every run read in detail (citable steps)
  workflows       {name: definition} — what exists, so a proposal can edit it
  memory          every memory entry at every scope: `step` (an entry with a
                  stepId), `workflow`, `project` (byProject) and `global` (all
                  projects together) — each entry's `id` is a citable memory key
  state           explicit state: global, per project, per workflow and step;
                  each var is citable as `state:<scope>:<name>:<key>`
  suppressed      proposal fingerprints not to propose again: `rejected` (a
                  person said no), `pending` (already waiting on a person)

The ONE write: a proposal a person REJECTED (an apply-mode run of this workflow
that ended cancelled) is remembered as a `rejected-proposal <fingerprint>` note
in this workflow's memory, so it is never proposed again.
"""
import datetime as dt
import json
import os
import sys
import urllib.parse
import urllib.request

SELF = os.environ.get("RETRO_WORKFLOW", "retrospective")
MAX_DETAIL = int(os.environ.get("MAX_RUN_DETAILS", "40"))


def api(path, method="GET", body=None):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + "/api" + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read() or b"null")


REJECTED, APPLIED = "rejected-proposal ", "applied-proposal "


def input_of(r):
    v = r.get("input") or {}
    if isinstance(v, str):
        try:
            v = json.loads(v)
        except ValueError:
            v = {}
    return v if isinstance(v, dict) else {}


def is_apply(r):
    return r.get("workflow") == SELF and input_of(r).get("mode") == "apply"


def fingerprints(entries, prefix):
    out = set()
    for m in entries:
        body = m.get("body") or ""
        if m.get("workflow") == SELF and body.startswith(prefix):
            out.add(body[len(prefix):].split(" ", 1)[0])
    return out


def suppressed(runs, memory):
    """Rejected proposals are remembered (written once, here) and never
    proposed again; pending ones are not proposed twice."""
    rejected = fingerprints(memory, REJECTED)
    pending = set()
    for r in runs:
        if not is_apply(r):
            continue
        fp = str(input_of(r).get("fingerprint") or "")
        if not fp:
            continue
        if r.get("status") == "cancelled" and fp not in rejected:
            p = input_of(r).get("proposal") or {}
            t = p.get("target") or {}
            api(f"/workflows/{urllib.parse.quote(SELF, safe='')}/memory", "POST",
                {"body": f"{REJECTED}{fp} {p.get('kind', '')} {t.get('workflow', '')}/{t.get('step', '')} (run {r.get('id')})"})
            rejected.add(fp)
        elif r.get("status") in ("awaiting_approval", "queued", "running", "pending"):
            pending.add(fp)
    return {"rejected": sorted(rejected), "pending": sorted(pending)}


def memory_and_state(wfs):
    """Memory at every scope and explicit state at every scope. A workflow
    whose memory cannot be read is skipped, never fatal."""
    entries, by_project, defs = [], {}, {}
    state = {"global": [], "project": {}, "workflow": {}}
    for w in wfs:
        n = w.get("name")
        if not n:
            continue
        q = urllib.parse.quote(n, safe="")
        try:
            defs[n] = (lambda v: v.get("definition", v) if isinstance(v, dict) else v)(api(f"/workflows/{q}"))
        except Exception:
            defs[n] = {"name": n, "description": w.get("description", "")}
        try:
            for m in listing(api(f"/workflows/{q}/memory"), "entries"):
                e = {"id": m.get("id"), "scope": "step" if m.get("stepId") else "workflow",
                     "project": m.get("project") or "", "workflow": m.get("workflow") or n,
                     "stepId": m.get("stepId") or "", "kind": m.get("kind"), "status": m.get("status") or "",
                     "runId": m.get("runId") or "", "body": (m.get("body") or "")[:600]}
                entries.append(e)
                by_project.setdefault(e["project"], set()).add(e["workflow"])
        except Exception:
            pass
        try:
            for v in listing(api(f"/workflows/{q}/state"), "vars"):
                if v.get("scope") != "global":
                    state["workflow"].setdefault(n, []).append(v)
        except Exception:
            pass
    try:
        state["global"] = listing(api("/state?scope=global"), "vars")
    except Exception:
        pass
    try:
        for p in listing(api("/projects"), "projects"):
            name = p.get("name") if isinstance(p, dict) else p
            if name:
                state["project"][name] = listing(api("/state?scope=project&name=" + urllib.parse.quote(name)), "vars")
    except Exception:
        pass
    memory = {"entries": entries, "byProject": {k: sorted(v) for k, v in by_project.items()},
              "scopes": "step = entries with a stepId; workflow = per workflow; project = byProject; global = all entries"}
    return memory, state, defs


def ts(v):
    if not v:
        return None
    try:
        return dt.datetime.fromisoformat(str(v).replace("Z", "+00:00"))
    except ValueError:
        return None


def listing(v, key):
    return (v.get(key) or []) if isinstance(v, dict) else (v or [])


def usage_of(step):
    u = step.get("usage") or {}
    if isinstance(u, str):
        try:
            u = json.loads(u)
        except ValueError:
            u = {}
    if not isinstance(u, dict):
        return 0, 0.0
    tokens = u.get("totalTokens") or ((u.get("inputTokens") or 0) + (u.get("outputTokens") or 0))
    return int(tokens or 0), float(u.get("costUsd") or 0)


def previous_findings(runs):
    """The newest finished retrospective's checked findings, and whether each
    finding's workflow failed again after it."""
    me = os.environ.get("WFX_RUN_ID", "")
    mine = sorted((r for r in runs if r.get("workflow") == SELF and r.get("status") == "done" and r.get("id") != me
                   and not is_apply(r)),
                  key=lambda r: r.get("createdAt") or "", reverse=True)
    if not mine:
        return {"runId": "", "at": "", "findings": []}
    last = mine[0]
    try:
        detail = api("/runs/" + last["id"])
        check = next((s for s in detail.get("steps") or [] if s.get("stepId") == "check"), None)
        out = (check or {}).get("output") or {}
        prior = json.loads(out.get("stdout") or "{}").get("findings") or []
    except Exception:
        prior = []
    at = ts(last.get("createdAt"))
    tracked = []
    for f in prior:
        again = [r["id"] for r in runs if r.get("workflow") == f.get("workflow") and r.get("status") == "failed"
                 and at is not None and (ts(r.get("createdAt")) or at) > at]
        tracked.append({**f, "status": "recurred" if again else "no new failure", "failedSince": again[:5]})
    return {"runId": last["id"], "at": last.get("createdAt", ""), "findings": tracked}


def main():
    try:
        i = json.load(open("input.json"))
    except Exception:
        i = {}
    now = dt.datetime.now(dt.timezone.utc)
    since = now - dt.timedelta(hours=float(i.get("since_hours") or os.environ.get("SINCE_HOURS", "24")))
    focus_wf, focus_run = (i.get("workflow") or "").strip(), (i.get("runId") or "").strip()

    try:
        runs = list(listing(api("/runs"), "runs"))
        if focus_wf:
            runs += listing(api("/runs?workflow=" + urllib.parse.quote(focus_wf)), "runs")
        if focus_run:
            runs.append(api("/runs/" + urllib.parse.quote(focus_run))["run"])
    except Exception as e:
        print(f"cannot read the platform API: {e}")
        return 2
    seen, uniq = set(), []
    for r in runs:
        if r.get("id") not in seen:
            seen.add(r.get("id"))
            uniq.append(r)
    runs = uniq

    def in_window(r):
        if r.get("workflow") == SELF:
            return False
        if focus_run and r.get("id") == focus_run:
            return True
        t = ts(r.get("createdAt"))
        return t is not None and t >= since and (not focus_wf or r.get("workflow") == focus_wf)

    window = [r for r in runs if in_window(r)]
    per = {}
    for r in window:
        w = per.setdefault(r["workflow"], {"total": 0, "status": {}, "retries": 0, "approvalWaitSec": 0, "tokens": 0, "costUsd": 0.0})
        w["total"] += 1
        w["status"][r.get("status")] = w["status"].get(r.get("status"), 0) + 1

    failures, retries, waits, stepmap = [], [], [], {}
    # Step details for the runs that matter most first: failed and paused.
    order = sorted(window, key=lambda r: (r.get("id") != focus_run,
                                          r.get("status") not in ("failed", "awaiting_approval", "needs_input"),
                                          r.get("createdAt") or ""))
    for r in order[:MAX_DETAIL]:
        try:
            steps = api("/runs/" + r["id"]).get("steps") or []
        except Exception as e:
            failures.append({"workflow": r["workflow"], "runId": r["id"], "stepId": "",
                             "error": f"(could not read the run: {e})", "attempts": 0, "at": r.get("createdAt", "")})
            continue
        w = per[r["workflow"]]
        stepmap[r["id"]] = [s.get("stepId") for s in steps]
        for s in steps:
            tok, cost = usage_of(s)
            w["tokens"] += tok
            w["costUsd"] = round(w["costUsd"] + cost, 6)
            if (s.get("attempts") or 0) > 1:
                w["retries"] += s["attempts"] - 1
                retries.append({"workflow": r["workflow"], "runId": r["id"], "stepId": s.get("stepId"),
                                "attempts": s["attempts"], "status": s.get("status")})
            if s.get("status") == "failed":
                failures.append({"workflow": r["workflow"], "runId": r["id"], "stepId": s.get("stepId"),
                                 "error": (s.get("error") or r.get("error") or "")[:600],
                                 "attempts": s.get("attempts") or 0, "at": r.get("createdAt", "")})
            if s.get("resolution") or s.get("status") in ("awaiting_approval", "needs_input"):
                start, end = ts(s.get("startedAt")), ts(s.get("resolvedAt"))
                sec = int(((end or now) - start).total_seconds()) if start else 0
                w["approvalWaitSec"] += sec
                waits.append({"workflow": r["workflow"], "runId": r["id"], "stepId": s.get("stepId"), "waitedSec": sec,
                              "resolution": s.get("resolution") or "still waiting", "by": s.get("resolvedBy") or ""})
        if r.get("status") == "failed" and not any(f["runId"] == r["id"] for f in failures):
            failures.append({"workflow": r["workflow"], "runId": r["id"], "stepId": r.get("currentStep") or "",
                             "error": (r.get("error") or "")[:600], "attempts": 0, "at": r.get("createdAt", "")})

    try:
        wfs = listing(api("/workflows"), "workflows")
        memory, state, defs = memory_and_state(wfs)
        held = suppressed(runs, memory["entries"])
    except Exception as e:
        print(f"cannot read the platform API: {e}")
        return 2
    facts = {
        "window": {"since": since.isoformat(), "until": now.isoformat()},
        "focus": {"workflow": focus_wf, "runId": focus_run, "question": i.get("question") or ""},
        "runs": per, "failures": failures, "retries": retries, "waits": waits,
        "previous": previous_findings(runs),
        "steps": stepmap, "workflows": defs, "memory": memory, "state": state, "suppressed": held,
    }
    with open("facts.json", "w") as f:
        json.dump(facts, f, indent=2)
    n = sum(w["total"] for w in per.values())
    print(f"{n} runs over {len(per)} workflows, {len(failures)} failed steps, {len(retries)} retried, "
          f"{len(waits)} waits, {len(facts['previous']['findings'])} earlier findings tracked, "
          f"{len(memory['entries'])} memory entries, {len(held['rejected'])} rejected proposals suppressed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
