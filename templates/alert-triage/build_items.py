#!/usr/bin/env python3
"""build_items.py — the classifier's input, built by CODE from the triage
step's typed output (triage.json), so what is judged is exactly what the
agent submitted. Writes items.jsonl, one item per triaged issue, with the
alert as filed and the related issues' titles beside the claim.
"""
import json
import os

t = json.load(open("triage.json"))
with open("items.jsonl", "w") as out:
    for x in t.get("issues", []):
        n = x["number"]
        d = os.path.join("evidence", str(n))
        issue = json.load(open(os.path.join(d, "issue.json"))) if os.path.exists(os.path.join(d, "issue.json")) else {}
        related = json.load(open(os.path.join(d, "related.json"))) if os.path.exists(os.path.join(d, "related.json")) else []
        titles = {r["number"]: f"#{r['number']} ({r['state'].lower()}): {r['title']}" for r in related}
        dup = x.get("duplicate_of") or 0
        state = (f"ALERT #{n}: {issue.get('title', '')}. As filed: {(issue.get('body') or '')[:1200]} "
                 f"TRIAGE VERDICT: {x['verdict']}" + (f" of #{dup} — {titles.get(dup, '(not in the shortlist)')}" if dup else "")
                 + f". Summary: {x['summary']} Cause: {x['cause']} Evidence: {' ; '.join(x['evidence'][:8])}. "
                 f"Still happening now: {x['still_happening']}. Owner: {x['owner']}. "
                 f"Other alerts considered: {' | '.join(titles.values()) or 'none'}.")
        out.write(json.dumps({"id": str(n), "state": state}) + "\n")
print(f"{len(t.get('issues', []))} triage(s) to judge")
