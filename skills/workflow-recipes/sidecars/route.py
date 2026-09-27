#!/usr/bin/env python3
"""route.py — split judged items into yes / uncertain / no by ONE noul question.

  python3 route.py <items.jsonl> <verdicts.jsonl> <question> [prefix]

With a prefix the files are <prefix>yes.jsonl etc., so a second question can
route the first one's yes list without overwriting it.

items.jsonl    what the agent wrote: {"id", "state", ...anything else (title, body)}
verdicts.jsonl what `wfx judge` wrote: {"id", "answers": {<q>: {"noul", "band"}, ...}}

Writes yes.jsonl, uncertain.jsonl, no.jsonl — each line the item plus its
answers. An item with NO verdict (the classifier failed, timed out, or the key
was missing) goes to uncertain, never to yes and never to no: a broken judge
must stop for a person, not act and not drop the finding.

Exit 3 = a person must decide (uncertain.jsonl); otherwise 0 = at least one
yes to act on, 4 = nothing to act on (all no). Guard act-steps on exitCode 0.
"""
import json
import sys

items_path, verdicts_path, q = sys.argv[1], sys.argv[2], sys.argv[3]
prefix = sys.argv[4] if len(sys.argv) > 4 else ""

items = [json.loads(line) for line in open(items_path) if line.strip()]
verdicts = {}
try:
    for line in open(verdicts_path):
        if line.strip():
            v = json.loads(line)
            verdicts[v["id"]] = v
except FileNotFoundError:
    pass

buckets = {"yes": [], "uncertain": [], "no": []}
for it in items:
    v = verdicts.get(it["id"])
    band = (((v or {}).get("answers") or {}).get(q) or {}).get("band")
    if band not in buckets:
        band = "uncertain"
        it["route_reason"] = "no verdict from the classifier"
    it["answers"] = (v or {}).get("answers", {})
    buckets[band].append(it)

for name, rows in buckets.items():
    with open(f"{prefix}{name}.jsonl", "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")

print(json.dumps({k: [r["id"] for r in v] for k, v in buckets.items()}))
sys.exit(3 if buckets["uncertain"] else (0 if buckets["yes"] else 4))
