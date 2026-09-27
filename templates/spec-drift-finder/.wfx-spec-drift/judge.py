#!/usr/bin/env python3
"""judge.py — a calibrated verdict for EVERY requirement, then the decision table, by code.

For each requirement (none skipped — an unmapped one is judged on the machine
hints and marked), build the evidence from files (sdlib.evidence), ask `wfx
judge` two questions (verdict-questions.yaml: verdict, action), then:

  verdict               recommendation            who decides
  conforms              none                      nobody (listed, collapsed)
  code_violates_spec    change_code               default APPROVED when the verdict is `sure`
                                                  and the classifier's action agrees; else a person
  spec_outdated         change_spec               a person (specs belong to the human)
  conflict              clarify / its action      a person
  unimplemented         its action (code|spec)    a person
  untestable_vague      clarify                   a person
  (classifier error)    clarify                   a person

The classifier never edits anything and never decides alone that a spec
changes. Writes verdicts.jsonl, plan.json, plan.md. Exit 0 when something does
not conform, 78 when everything conforms (the run ends with a report), 70 on error.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D, answer, evidence, judge, load_jsonl, requirements  # noqa: E402

REC = {"conforms": "none", "code_violates_spec": "change_code", "spec_outdated": "change_spec",
       "conflict": "clarify", "unimplemented": "clarify", "untestable_vague": "clarify"}


def main():
    reqs = requirements()
    maps = {m["id"]: m for m in load_jsonl(f"{D}/mapping.jsonl")}
    items = [{"id": rid, "state": evidence(r, maps.get(rid), reqs)} for rid, r in reqs.items()]
    with open(f"{D}/evidence.jsonl", "w") as f:
        for it in items:
            f.write(json.dumps(it) + "\n")
    res = judge(items)

    plan = []
    for rid, r in reqs.items():
        v, vband, vconf = answer(res.get(rid), "verdict")
        a, aband, _ = answer(res.get(rid), "action")
        m = maps.get(rid)
        row = {"id": rid, "where": r["where"], "spec": r["spec"], "spec_kind": r["spec_kind"], "kind": r["kind"],
               "section": r.get("section", ""), "text": r["text"], "mapped": m is not None,
               "mapping_status": (m or {}).get("status"), "code_refs": (m or {}).get("code_refs", []),
               "test_refs": (m or {}).get("test_refs", []), "verdict": v or "unjudged", "verdict_band": vband,
               "verdict_confidence": vconf, "action": a, "action_band": aband}
        if not v:
            row.update(recommendation="clarify", needs_human=True, default=None,
                       why="the classifier returned no verdict: " + str((res.get(rid) or {}).get("error", "missing")))
        elif v == "conforms":
            row.update(recommendation="none", needs_human=False, default="none", why="conforms")
        else:
            rec = REC[v]
            if v in ("unimplemented", "conflict") and a in ("change_code", "change_spec"):
                rec = a
            confident_code = v == "code_violates_spec" and vband == "sure" and a == "change_code"
            row.update(recommendation=rec, needs_human=not confident_code,
                       default="change_code" if confident_code else None,
                       why=f"verdict {v} ({vband}, {vconf}); classifier action {a} ({aband})")
        plan.append(row)

    json.dump({"plan": plan}, open(f"{D}/plan.json", "w"), indent=1)
    with open(f"{D}/verdicts.jsonl", "w") as f:
        for p in plan:
            f.write(json.dumps(p) + "\n")

    counts = {}
    for p in plan:
        counts[p["verdict"]] = counts.get(p["verdict"], 0) + 1
    non = [p for p in plan if p["verdict"] != "conforms"]
    md = [f"# Spec drift — {len(plan)} requirements: " + ", ".join(f"{n} {k}" for k, n in sorted(counts.items())), "",
          f"Unmapped (judged on machine hints only): {sum(1 for p in plan if not p['mapped'])}", "",
          "| id | verdict | recommendation | decided by | requirement | where |", "|---|---|---|---|---|---|"]
    order = {"code_violates_spec": 0, "spec_outdated": 1, "conflict": 2, "unimplemented": 3,
             "untestable_vague": 4, "unjudged": 5}
    for p in sorted(non, key=lambda p: (order.get(p["verdict"], 9), p["id"])):
        who = "default approved (confident)" if p["default"] == "change_code" else "a person"
        md.append(f"| {p['id']} | **{p['verdict']}** {p['verdict_band'] or ''} | {p['recommendation']} | {who} | "
                  f"{p['text'][:110].replace('|', '/')} | {p['where']} |")
    open(f"{D}/plan.md", "w").write("\n".join(md) + "\n")
    print("\n".join(md))
    if not non:
        print("\nEvery requirement conforms — nothing to change, no PR.")
        sys.exit(78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"judge.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
