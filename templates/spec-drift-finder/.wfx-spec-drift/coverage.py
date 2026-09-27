#!/usr/bin/env python3
"""coverage.py — did the mappers read EVERY requirement? Decided by code, not by the agent's word.

Merges every mapping the mappers wrote (.wfx-spec-drift/map/*.json[l], plus the
gap-filler's typed output in gaps.json when present) into mapping.jsonl, keyed
by requirement id; drops cited locations that do not exist (they are listed, not
trusted); and writes the ids nobody mapped to missing.txt.

  exit 0   some requirements are unmapped -> the gap-filling step runs for exactly those
  exit 78  every requirement has a reading -> skip straight to the judge
  exit 70  error
With --final it never asks for another pass: it reports what is still unmapped
(the judge then judges those on the machine hints alone and marks them).
"""
import glob
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D, load_jsonl, parse_ref, requirements  # noqa: E402

STATUSES = {"implemented", "partial", "missing", "not_code"}


def records_from(path):
    txt = open(path, encoding="utf-8", errors="replace").read().strip()
    if not txt:
        return []
    try:
        doc = json.loads(txt)
        if isinstance(doc, dict):
            doc = doc.get("mappings") or doc.get("requirements") or [doc]
        return doc if isinstance(doc, list) else []
    except ValueError:
        return load_jsonl(path)


def main():
    final = "--final" in sys.argv
    reqs = requirements()
    merged, bad_refs = {}, []
    sources = sorted(glob.glob(f"{D}/map/*.json") + glob.glob(f"{D}/map/*.jsonl"))
    if os.path.exists(f"{D}/gaps.json"):
        sources.append(f"{D}/gaps.json")
    for src in sources:
        for m in records_from(src):
            if not isinstance(m, dict):
                continue
            rid = str(m.get("id", "")).strip()
            if rid not in reqs:
                continue
            status = str(m.get("status", "")).strip().lower()
            if status not in STATUSES:
                status = "missing" if not (m.get("code_refs") or []) else "partial"
            good = {}
            for k in ("code_refs", "test_refs"):
                refs = m.get(k) or []
                if isinstance(refs, str):
                    refs = [refs]
                keep = []
                for r in refs:
                    p = parse_ref(r)
                    if p and os.path.isfile(p[0]):
                        keep.append(f"{p[0]}:{p[1]}" + (f"-{p[2]}" if p[2] != p[1] else ""))
                    else:
                        bad_refs.append(f"{rid}: {r}")
                good[k] = keep
            rec = {"id": rid, "status": status, **good,
                   "notes": str(m.get("notes", ""))[:1200], "moved_on": str(m.get("moved_on", ""))[:600],
                   "source": os.path.basename(src)}
            prev = merged.get(rid)
            # a later, richer reading wins over an earlier empty one
            if prev is None or (len(rec["code_refs"]) + len(rec["test_refs"]) >= len(prev["code_refs"]) + len(prev["test_refs"])):
                merged[rid] = rec
    with open(f"{D}/mapping.jsonl", "w") as f:
        for rid in reqs:
            if rid in merged:
                f.write(json.dumps(merged[rid]) + "\n")
    missing = [rid for rid in reqs if rid not in merged]
    open(f"{D}/missing.txt", "w").write("\n".join(missing) + ("\n" if missing else ""))
    by_status = {}
    for m in merged.values():
        by_status[m["status"]] = by_status.get(m["status"], 0) + 1
    print(f"{len(merged)}/{len(reqs)} requirements have a mapper reading · by status {by_status}")
    if bad_refs:
        print(f"{len(bad_refs)} cited locations do not exist and were dropped: " + "; ".join(bad_refs[:12]))
    if missing:
        print(f"UNMAPPED ({len(missing)}): " + " ".join(missing))
        if final:
            print("these are judged on the machine hints alone and marked `unmapped` in the plan")
            sys.exit(0)
        sys.exit(0)
    sys.exit(78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"coverage.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
