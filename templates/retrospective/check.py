#!/usr/bin/env python3
"""retrospective: keep only findings that cite real evidence. No invented runs.

    check.py   reads facts.json + analysis.json; prints the typed result
               {findings, summary, dropped, fixTracking} on stdout; exit 0,
               or 2 when the analysis is malformed (the run fails; never a silent "all fine")

A finding must name a workflow in the evidence. A runId, when given, must be a
run of that workflow in the evidence; a stepId given with it must be a step
that failed, retried or waited there. Anything else is DROPPED and listed.
"""
import json
import os
import sys


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


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

    tracking = [{"workflow": p.get("workflow"), "what": p.get("what"), "status": p.get("status")} for p in previous]
    print(json.dumps({"findings": keep, "summary": summary, "dropped": dropped, "fixTracking": tracking}, indent=2))
    recurred = sum(1 for t in tracking if t["status"] == "recurred")
    outcome(f"{'incident' if keep else 'ok'}: {len(keep)} findings, {len(dropped)} dropped, "
            f"{recurred} earlier fixes did not hold")
    return 0


if __name__ == "__main__":
    sys.exit(main())
