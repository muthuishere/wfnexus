#!/usr/bin/env python3
"""build_items.py — the classifier's input, built by CODE from the triage
step's typed output (triage.json), never from files the agent chose to write.
What the classifier judges is then exactly what the agent submitted.

Writes findings.jsonl (one item per finding) and duplicates.jsonl (one per
finding the agent mapped onto an existing open issue).
"""
import json
import os

t = json.load(open("triage.json"))
known = {}
if os.path.exists("known_issues.json"):
    known = {i["number"]: i["title"] for i in json.load(open("known_issues.json"))}

with open("findings.jsonl", "w") as f, open("duplicates.jsonl", "w") as d:
    for x in t.get("findings", []):
        state = (f"{x['title']}. {x['summary']} Impact: {x['impact']} "
                 f"Evidence: {' ; '.join(x['evidence'])}. Probable cause: {x['probable_cause']} "
                 f"In this product's scope: {'yes' if x.get('in_scope', True) else 'no — ' + x.get('scope_note', '')}.")
        f.write(json.dumps({"id": x["fingerprint"], "state": state}) + "\n")
        n = x.get("existing_issue") or 0
        if n:
            d.write(json.dumps({"id": x["fingerprint"], "state":
                                f"NEW FINDING: {x['title']}. {x['summary']} Evidence: {' ; '.join(x['evidence'][:4])}. "
                                f"Probable cause: {x['probable_cause']} EXISTING ISSUE #{n}: {known.get(n, '(title not in the open list)')}"}) + "\n")
print(f"{len(t.get('findings', []))} finding(s) to judge")
