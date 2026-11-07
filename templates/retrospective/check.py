#!/usr/bin/env python3
"""retrospective: keep only findings that cite real evidence. No invented runs.

    check.py   reads facts.json + analysis.json; prints the typed result
               {findings, summary, dropped, fixTracking} on stdout; exit 0,
               or 2 when the analysis is malformed (the run fails; never a silent "all fine")

A finding must name a workflow in the evidence. A runId, when given, must be a
run of that workflow in the evidence; a stepId given with it must be a step
that failed, retried or waited there. Anything else is DROPPED and listed.

A PROPOSAL {kind, target{project, workflow, step}, rationale, evidence{runIds,
stepIds, memoryKeys}, change} is kept only when:
  - kind is add-step | edit-step | new-workflow | memory-fix | retire-workflow
  - it cites at least one piece of evidence and EVERY citation is real: a run
    read this window, a step of one of the cited runs, a memory entry id or a
    `state:<scope>:<name>:<key>` from facts.json
  - its target fits the kind (new-workflow names a workflow that does not
    exist; the others one that does; edit-step an existing step, add-step a
    new one) and its change is the exact change: `definition` (the whole new
    workflow, as JSON) for add/edit/new, `memory` {op add|edit|delete, id,
    body} for memory-fix, nothing for retire
  - its fingerprint (kind + target) was not rejected before and is not
    already waiting on a person
"""
import hashlib
import json
import os
import sys


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


KINDS = ("add-step", "edit-step", "new-workflow", "memory-fix", "retire-workflow")


def fingerprint(kind, target, change):
    """What a person said yes or no to: the kind and the target (and the memory
    entry, for a memory fix) — not the wording, so a reworded rejected
    proposal is still recognised."""
    mem = (change or {}).get("memory") or {} if isinstance(change, dict) else {}
    key = "|".join([kind, target.get("project") or "", target.get("workflow") or "", target.get("step") or "",
                    str(mem.get("op") or ""), str(mem.get("id") or "")])
    return hashlib.sha256(key.encode()).hexdigest()[:12]


def step_ids(definition):
    out = set()
    for s in (definition or {}).get("steps") or []:
        if isinstance(s, dict):
            out.add(s.get("id"))
            out |= step_ids(s)
    return out


def check_proposals(facts, raw):
    runs = set()
    for k in ("failures", "retries", "waits"):
        runs |= {x.get("runId") for x in facts.get(k) or []}
    stepmap = facts.get("steps") or {}
    runs |= set(stepmap)
    mem = facts.get("memory") or {}
    entries = {m.get("id"): m for m in mem.get("entries") or []}
    keys = set(entries)
    st = facts.get("state") or {}
    for v in st.get("global") or []:
        keys.add(f"state:global::{v.get('key')}")
    for scope in ("project", "workflow"):
        for name, vs in (st.get(scope) or {}).items():
            for v in vs or []:
                keys.add(f"state:{v.get('scope') or scope}:{v.get('scopeName') or name}:{v.get('key')}")
    defs = facts.get("workflows") or {}
    held = facts.get("suppressed") or {}
    blocked = {fp: "rejected before" for fp in held.get("rejected") or []}
    blocked.update({fp: "already waiting on a person" for fp in held.get("pending") or []})

    keep, dropped = [], []
    for p in raw:
        why = ""
        if not isinstance(p, dict):
            dropped.append({"proposal": p, "why": "not an object"})
            continue
        kind = p.get("kind")
        t = p.get("target") if isinstance(p.get("target"), dict) else {}
        ev = p.get("evidence") if isinstance(p.get("evidence"), dict) else {}
        ch = p.get("change") if isinstance(p.get("change"), dict) else {}
        wf, step = t.get("workflow") or "", t.get("step") or ""
        cited_runs, cited_steps, cited_keys = (list(ev.get(k) or []) for k in ("runIds", "stepIds", "memoryKeys"))
        run_steps = set()
        for r in cited_runs:
            run_steps |= set(stepmap.get(r) or [])
        for k in ("failures", "retries", "waits"):
            run_steps |= {x.get("stepId") for x in facts.get(k) or [] if x.get("runId") in cited_runs}
        d = ch.get("definition") if isinstance(ch.get("definition"), dict) else None
        m = ch.get("memory") if isinstance(ch.get("memory"), dict) else None
        if kind not in KINDS:
            why = f"kind {kind!r} is not one of {', '.join(KINDS)}"
        elif not (cited_runs or cited_steps or cited_keys):
            why = "no evidence cited"
        elif any(r not in runs for r in cited_runs):
            why = f"run(s) {[r for r in cited_runs if r not in runs]} are not in the evidence"
        elif any(s not in run_steps for s in cited_steps):
            why = f"step(s) {[s for s in cited_steps if s not in run_steps]} are not steps of the cited runs"
        elif any(k not in keys for k in cited_keys):
            why = f"memory key(s) {[k for k in cited_keys if k not in keys]} are not in the evidence"
        elif not wf:
            why = "no target workflow"
        elif kind == "new-workflow" and wf in defs:
            why = f"workflow {wf!r} already exists"
        elif kind != "new-workflow" and wf not in defs:
            why = f"workflow {wf!r} does not exist"
        elif not str(p.get("rationale") or "").strip():
            why = "no rationale"
        elif kind in ("add-step", "edit-step", "new-workflow") and (d is None or d.get("name") != wf or not d.get("steps")):
            why = "change.definition must be the whole new workflow, named as the target, with steps"
        elif kind in ("add-step", "edit-step") and not step:
            why = "no target step"
        elif kind == "edit-step" and (step not in step_ids(defs.get(wf)) or step not in step_ids(d)):
            why = f"step {step!r} is not a step of {wf}"
        elif kind == "add-step" and (step in step_ids(defs.get(wf)) or step not in step_ids(d)):
            why = f"step {step!r} already exists in {wf}, or the new definition does not add it"
        elif kind == "memory-fix" and (m is None or m.get("op") not in ("add", "edit", "delete")):
            why = "change.memory needs op add, edit or delete"
        elif kind == "memory-fix" and m.get("op") != "add" and (m.get("id") not in entries or entries[m["id"]].get("workflow") != wf):
            why = f"memory entry {m.get('id')!r} is not an entry of {wf}"
        elif kind == "memory-fix" and m.get("op") != "delete" and not str(m.get("body") or "").strip():
            why = "a memory add or edit needs a body"
        if not why:
            fp = fingerprint(kind, t, ch)
            why = blocked.get(fp, "")
        if why:
            dropped.append({"proposal": p, "why": why})
            continue
        keep.append({"kind": kind, "target": {"project": t.get("project") or "", "workflow": wf, "step": step},
                     "rationale": p.get("rationale"),
                     "evidence": {"runIds": cited_runs, "stepIds": cited_steps, "memoryKeys": cited_keys},
                     "change": ({"definition": d} if d is not None else {"memory": m} if m is not None else {}),
                     "fingerprint": fp})
        blocked[fp] = "proposed twice in one run"
    return keep, dropped


def main():
    try:
        facts = json.load(open("facts.json"))
        a = json.load(open("analysis.json"))
        raw = a["findings"]
        summary = str(a["summary"])
        if not isinstance(raw, list):
            raise ValueError("findings is not a list")
    except Exception as e:
        print(f"the analysis is malformed: {e!r}")
        return 2
    workflows = set(facts.get("runs") or {})
    cited = {}
    for k in ("failures", "retries", "waits"):
        for x in facts.get(k) or []:
            c = cited.setdefault(x.get("runId"), {"workflow": x.get("workflow"), "steps": set()})
            c["steps"].add(x.get("stepId"))
    previous = (facts.get("previous") or {}).get("findings") or []
    for p in previous:
        workflows.add(p.get("workflow"))

    keep, dropped = [], []
    for f in raw:
        if not isinstance(f, dict):
            dropped.append({"finding": f, "why": "not an object"})
            continue
        wf, run, step = f.get("workflow", ""), f.get("runId", ""), f.get("stepId", "")
        why = ""
        if wf not in workflows:
            why = f"workflow {wf!r} is not in the evidence"
        elif run and (run not in cited or cited[run]["workflow"] != wf):
            why = f"run {run!r} is not a cited run of {wf}"
        elif run and step and step not in cited[run]["steps"]:
            why = f"step {step!r} did not fail, retry or wait in run {run}"
        elif not (f.get("what") and f.get("fix")):
            why = "no what or no fix"
        if why:
            dropped.append({"finding": f, "why": why})
        else:
            keep.append({k: f.get(k, "") for k in ("workflow", "runId", "stepId", "what", "why", "fix")})

    proposals, pdropped = check_proposals(facts, a.get("proposals") or [])
    dropped += pdropped

    tracking = [{"workflow": p.get("workflow"), "what": p.get("what"), "status": p.get("status")} for p in previous]
    print(json.dumps({"findings": keep, "proposals": proposals, "summary": summary,
                      "dropped": dropped, "fixTracking": tracking}, indent=2))
    recurred = sum(1 for t in tracking if t["status"] == "recurred")
    outcome(f"{'incident' if keep else 'ok'}: {len(keep)} findings, {len(dropped)} dropped, "
            f"{recurred} earlier fixes did not hold")
    return 0


if __name__ == "__main__":
    sys.exit(main())
