#!/usr/bin/env python3
"""frontdesk: turn the agent's answer into exactly one of three moves.

    exit 0   reply      (the answer is confident)
    exit 10  hand off to a workflow — stdout is its name, from HANDOFF_WORKFLOWS only
    exit 11  hand off to a person   — the draft waits for approval
    exit 2   the answer is malformed (fails the run; never a silent drop)

The model may NAME a workflow, but only one on the HANDOFF_WORKFLOWS list is
ever started; anything else goes to a person.
"""
import json
import os
import sys


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


def main():
    try:
        a = json.load(open("answer.json"))
        conf = float(a.get("confidence"))
    except Exception as e:
        print(f"the answer is malformed: {e}")
        sys.exit(2)
    floor = float(os.environ.get("MIN_CONFIDENCE", "0.7"))
    allowed = [w.strip() for w in os.environ.get("HANDOFF_WORKFLOWS", "").split(",") if w.strip()]
    to = (a.get("handoff_to") or "none").strip()
    wf = (a.get("handoff_workflow") or "").strip()
    reply = (a.get("reply") or "").strip()

    if to == "workflow" and wf in allowed:
        outcome(f"ok: handed off to {wf} — {a.get('reason', '')}")
        print(wf, end="")
        sys.exit(10)
    if to == "none" and reply and conf >= floor:
        outcome(f"ok: answered (confidence {conf:.2f})")
        print("reply", end="")
        sys.exit(0)
    why = (f"asked for workflow {wf!r}, which is not on HANDOFF_WORKFLOWS" if to == "workflow"
           else f"confidence {conf:.2f} < {floor}" if to == "none" else a.get("reason", "the agent asked for a person"))
    outcome(f"waiting: a person decides — {why}")
    print(f"person: {why}", end="")
    sys.exit(11)


if __name__ == "__main__":
    main()
