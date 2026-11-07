#!/usr/bin/env python3
"""frontdesk: turn the agent's answer into exactly one move.

    exit 0   reply      (the answer is confident, and there is a chat to reply in)
    exit 10  hand off to a workflow — stdout is its name, from HANDOFF_WORKFLOWS only
    exit 11  hand off to a person   — the draft waits for approval
    exit 12  diagnose — "why did X fail" goes to DIAGNOSE_WORKFLOW (retrospective)
             with start: … wait: true, and its answer is the reply
    exit 20  answered in the run    — no chat (a manual run or an MCP call): the
             reply is the `answer` step's output, nothing is sent anywhere
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


def has_chat():
    """A chat to reply in. Without context.json (older callers) assume one."""
    try:
        return bool(json.load(open("context.json")).get("chat"))
    except (OSError, ValueError):
        return True


def main():
    try:
        a = json.load(open("answer.json"))
        conf = float(a.get("confidence"))
    except Exception as e:
        print(f"the answer is malformed: {e}")
        sys.exit(2)
    floor = float(os.environ.get("MIN_CONFIDENCE", "0.7"))
    allowed = [w.strip() for w in os.environ.get("HANDOFF_WORKFLOWS", "").split(",") if w.strip()]
    diagnose = os.environ.get("DIAGNOSE_WORKFLOW", "retrospective").strip()
    to = (a.get("handoff_to") or "none").strip()
    wf = (a.get("handoff_workflow") or "").strip()
    reply = (a.get("reply") or "").strip()
    chat = has_chat()

    if to == "diagnose" and diagnose:
        target = (a.get("diagnose_workflow") or "").strip() or "every workflow"
        outcome(f"ok: asked {diagnose} why {target} failed")
        print(diagnose, end="")
        sys.exit(12)
    if not chat:
        outcome(f"ok: answered in the run (confidence {conf:.2f}, {to})")
        print("answered", end="")
        sys.exit(20)
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
