#!/usr/bin/env python3
"""claims.py — every claim in the PR body, checked against evidence the WRITER did not author.

cleanup-pr-writer checks its own claims; this re-checks them independently.
The writer lists its claims (claims.jsonl: {id, claim, about: [candidate ids |
"verification"]}); the EVIDENCE attached here comes from the machine's files —
the checks, the classifier's verdicts and verify.json — never from the writer.
Then:

  * `wfx judge` asks "is the CLAIM fully supported by the EVIDENCE?" per claim;
  * the body must quote the real numbers: before/after test counts, lines
    deleted, and every removal commit's SHA;
  * every approved removal that was committed must appear in the body.

Exit 0 when every claim is `supported: yes` and the numbers match; 20 when not
(the run stops for a person with the list); 70 on an error.
"""
import json
import os
import subprocess
import sys

D = ".wfx-code-remover"


def main():
    body = open(f"{D}/pr-body.md").read()
    claims = [json.loads(l) for l in open(f"{D}/claims.jsonl") if l.strip()]
    evidence = {json.loads(l)["id"]: json.loads(l)["state"] for l in open(f"{D}/evidence.jsonl") if l.strip()}
    plan = {r["id"]: r for r in json.load(open(f"{D}/plan.json"))["plan"]}
    v = json.load(open(f"{D}/verify.json"))
    vtext = (f"Suite before: {v['before']}. Suite after: {v['after']} (green: {v['suite_after_ok']}). "
             f"Commits: " + "; ".join(f"{c['sha']} {c['subject']} (−{c['deleted']}/+{c['added']})" for c in v["commits"]) +
             f". Lines deleted {v['lines_deleted']}, added {v['lines_added']}. Orphan tests named: {v['orphan_tests_named']}. "
             f"Newly unreachable after the removals: {', '.join(v['newly_unreachable']) or 'none'}. "
             f"Reverted: {', '.join(str(r.get('symbol')) for r in v['reverted']) or 'none'}.")
    items = []
    for c in claims:
        ev = []
        for a in c.get("about") or []:
            if a == "verification":
                ev.append(vtext)
            elif a in plan:
                r = plan[a]
                ev.append(evidence.get(a, "") + f"\nDecision: {r['verdict']} — {r.get('why', '')}. "
                          f"Classifier unused={r.get('unused')} ({r.get('unused_band')}), kind={r.get('kind_choice')} ({r.get('kind_band')}).")
        if not ev:
            ev.append("(the claim names no candidate or verification record — nothing supports it)")
        items.append({"id": c["id"], "state": f"CLAIM: {c['claim']}\nEVIDENCE: " + "\n".join(ev)})
    with open(f"{D}/claims.checked.jsonl", "w") as f:
        for it in items:
            f.write(json.dumps(it) + "\n")
    j = subprocess.run(["wfx", "judge", "-q", f"{D}/claim-questions.yaml", "--items", f"{D}/claims.checked.jsonl"],
                       capture_output=True, text=True, timeout=600)
    if j.returncode:
        print("the classifier could not check the claims: " + j.stderr.strip()[:500])
        sys.exit(70)
    res = [json.loads(l) for l in j.stdout.splitlines() if l.strip()]
    unsupported = [(r["id"], r["answers"]["supported"]["noul"], r["answers"]["supported"]["band"])
                   for r in res if (r.get("answers") or {}).get("supported", {}).get("band") != "yes"]

    missing = []
    for key, val in (("before pass count", v["before"].get("pass")), ("after pass count", v["after"].get("pass")),
                     ("lines deleted", v["lines_deleted"])):
        if val is not None and str(val) not in body:
            missing.append(f"{key} {val} is not quoted in the body")
    for c in v["commits"]:
        if c["sha"][:7] not in body:
            missing.append(f"commit {c['sha'][:7]} ({c['symbol']}) is not in the body")

    json.dump({"claims": len(res), "unsupported": unsupported, "missing": missing},
              open(f"{D}/claims-result.json", "w"), indent=1)
    print(f"{len(res)} claims checked by the classifier: {len(res) - len(unsupported)} supported (yes)")
    for cid, p, band in unsupported:
        print(f"  NOT SUPPORTED {cid}: supported={p} ({band})")
    for m in missing:
        print(f"  NUMBER MISSING: {m}")
    if unsupported or missing:
        sys.exit(20)
    print("every claim is backed by the evidence and every number is quoted.")


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"claims.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
