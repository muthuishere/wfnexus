#!/usr/bin/env python3
"""claims.py — every "change" the PR claims is checked against the commit it names.

The body is written by code, so its numbers are real; what is left to trust is
the ACTING agent's own one-line summary of each change. For each changed
requirement a calibrated classifier (JEV) reads the requirement, the decision,
the summary and the commit's actual diff, and answers: does the diff do what the
summary says, and nothing unrelated? Also checks the body names every commit.

Exit 0 when all hold, 20 when a claim is not supported (the step's needs_input
gate lists which), 70 on error.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D, git, judge, load_jsonl  # noqa: E402

Q = f"{D}/claim-questions.yaml"


def main():
    rows = {r["id"]: r for r in json.load(open(f"{D}/decisions.json"))["decisions"]}
    acts = {a["id"]: a for a in load_jsonl(f"{D}/act.jsonl") if a.get("id")}
    ver = json.load(open(f"{D}/verify.json"))
    body = open(f"{D}/pr-body.md").read()
    problems = []
    for c in ver["commits"]:
        if c["sha"] not in body:
            problems.append(f"the body does not name commit {c['sha']}")
    by_id = {}
    for c in ver["commits"]:
        if c["id"]:
            by_id.setdefault(c["id"], []).append(c["sha"])
    items = []
    for rid, shas in by_id.items():
        r, a = rows.get(rid, {}), acts.get(rid, {})
        diff = git("show", "--format=%h %s", "--unified=2", *shas)[:6000]
        items.append({"id": rid, "state":
                      f"Requirement {rid} ({r.get('where')}): \"{r.get('text', '')[:500]}\"\n"
                      f"Verdict before: {r.get('verdict')}. Decision: {(r.get('decision') or {}).get('decision')}.\n"
                      f"The PR claims this change: \"{a.get('summary', '(no summary)')}\"\n"
                      f"The actual commit diff:\n{diff}"})
    res = judge(items, Q) if items else {}
    out = []
    for it in items:
        a = (res.get(it["id"]) or {}).get("answers", {}).get("supported") or {}
        out.append({"id": it["id"], "supported": a.get("noul"), "band": a.get("band")})
        if a.get("band") != "yes":
            problems.append(f"{it['id']}: the diff does not clearly do what the PR says (supported={a.get('noul')}, {a.get('band')})")
    json.dump({"claims": out, "problems": problems}, open(f"{D}/claims.json", "w"), indent=1)
    print(f"{len(out)} change claims checked: " + ", ".join(f"{c['id']}={c['supported']:.2f}" if c['supported'] is not None
                                                           else f"{c['id']}=?" for c in out))
    if problems:
        print("NOT SUPPORTED:\n  " + "\n  ".join(problems))
        import re
        raw = open(f"{D}/answers.txt").read() if os.path.exists(f"{D}/answers.txt") else ""
        said = " ".join(c.split("\nA: ", 1)[1] for c in re.split(r"(?:^|\n)Q: ", raw) if "\nA: " in c)
        if "accept-claims" in said:
            with open(f"{D}/pr-body.md", "a") as f:
                f.write("\n**Claims a person accepted although the classifier did not confirm them:** "
                        + "; ".join(problems) + "\n")
            print("accepted by a person (accept-claims) — the PR body now says so")
            sys.exit(0)
        sys.exit(20)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"claims.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
