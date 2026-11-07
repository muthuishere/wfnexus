#!/usr/bin/env python3
"""frontdesk: turn retrospective's checked findings into one short reply.

    explain.py   reads diagnosis.json (the diagnose step's output: retrospective's
                 last step, propose, whose stdout is {summary, findings, pending, failed}) and
                 context.json; prints the reply.
                 exit 0 there is a chat to send it to · 20 no chat (the reply
                 is this step's stdout) · 2 the diagnosis is unreadable
"""
import json
import sys


def main():
    try:
        d = json.load(open("diagnosis.json"))
        r = json.loads(d.get("stdout") or "{}") if isinstance(d, dict) and "stdout" in d else d
        findings, summary = r.get("findings") or [], (r.get("summary") or "").strip()
    except Exception as e:
        print(f"the diagnosis is unreadable: {e}")
        return 2
    lines = [summary or "No summary."]
    for f in findings[:5]:
        cite = " / ".join(x for x in (f.get("workflow"), f.get("runId"), f.get("stepId")) if x)
        lines.append(f"- {f.get('what', '')} ({cite}). Why: {f.get('why', '')}. Fix: {f.get('fix', '')}")
    if not findings:
        lines.append("No failure with evidence in the window.")
    pending = r.get("pending") or []
    if pending:
        lines.append(f"{len(pending)} proposed change(s) wait for approval: "
                     + ", ".join(f"{p.get('kind')} {(p.get('target') or {}).get('workflow', '')} (run {p.get('runId')})" for p in pending[:5]))
    print("\n".join(lines))
    try:
        chat = json.load(open("context.json")).get("chat")
    except (OSError, ValueError):
        chat = ""
    return 0 if chat else 20


if __name__ == "__main__":
    sys.exit(main())
