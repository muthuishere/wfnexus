#!/usr/bin/env python3
"""gate.py — the human decision on every non-conforming requirement. Specs belong to the human.

Inputs: plan.json (the judge's verdicts), recommend.json (the recommender's reason
and smallest edit per item), answers.txt (every `wfx answer` given to this step,
as the platform folds them into input.answers) and decisions.txt (the `decisions`
run input, for scheduled runs that decide up front).

Every item whose `needs_human` is true — anything that would change a spec, every
conflict, every unimplemented or vague requirement, every unsure verdict — must
get an explicit decision: change_code, change_spec or skip. A confident
code_violates_spec item defaults to change_code but is listed too, and a person
can override it.

Answer syntax (any separator):   R012=change_code  R031=change_spec  R040=skip
                                  rest=skip | rest=recommended      (everything still open)
A reason may follow in parentheses: R031=change_spec (the 5000ms default was dropped on purpose)

Exit 0 with a complete set of decisions (decisions.json), 20 when a person still
has to decide (the step's needs_input gate shows the table), 78 when every
decision is skip (nothing to change), 70 on error.
"""
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D  # noqa: E402

CHOICES = {"change_code", "change_spec", "skip", "clarify"}
PAIR = re.compile(r"\b(R\d{3}|rest|all)\s*[=:]\s*(change_code|change_spec|skip|clarify|recommended)\b(?:\s*\(([^)]*)\))?", re.I)


def human_answers():
    """Only the A: parts of input.answers — the Q: parts are this step's own table."""
    txt = open(f"{D}/answers.txt").read() if os.path.exists(f"{D}/answers.txt") else ""
    parts = []
    for chunk in re.split(r"(?:^|\n)Q: ", txt):
        if "\nA: " in chunk:
            parts.append(chunk.split("\nA: ", 1)[1])
    if os.path.exists(f"{D}/decisions.txt"):
        parts.insert(0, open(f"{D}/decisions.txt").read())
    return parts


def main():
    plan = json.load(open(f"{D}/plan.json"))["plan"]
    rec = {}
    if os.path.exists(f"{D}/recommend.json"):
        try:
            for it in (json.load(open(f"{D}/recommend.json")) or {}).get("items", []):
                rec[it.get("id")] = it
        except ValueError:
            pass
    items = [p for p in plan if p["verdict"] != "conforms"]
    decided = {}
    for text in human_answers():  # later answers override earlier ones
        for rid, choice, why in PAIR.findall(text):
            rid, choice = rid.upper() if rid.lower() not in ("rest", "all") else rid.lower(), choice.lower()
            if rid in ("rest", "all"):
                for p in items:
                    if rid == "all" or p["id"] not in decided:
                        c = choice
                        if c == "recommended":
                            c = (rec.get(p["id"], {}).get("recommendation") or p["recommendation"])
                            c = c if c in ("change_code", "change_spec") else "skip"
                        decided[p["id"]] = {"decision": c, "by": "human", "why": why.strip() or f"{rid}={choice}"}
                continue
            if choice == "recommended":
                p = next((x for x in items if x["id"] == rid), None)
                c = (rec.get(rid, {}).get("recommendation") or (p or {}).get("recommendation") or "skip")
                choice = c if c in ("change_code", "change_spec") else "skip"
            decided[rid] = {"decision": choice, "by": "human", "why": why.strip()}

    rows, open_items = [], []
    for p in items:
        r = rec.get(p["id"], {})
        d = decided.get(p["id"])
        if d is None and not p["needs_human"] and p.get("default"):
            d = {"decision": p["default"], "by": "default (confident classifier)", "why": p["why"]}
        if d is None:
            open_items.append(p)
        if d and d["decision"] == "clarify":
            d = {**d, "decision": "skip", "why": ("clarify: " + d["why"]).strip()}
        rows.append({**p, "reason": r.get("reason", ""), "smallest_edit": r.get("smallest_edit", ""),
                     "recommender_says": r.get("recommendation", ""), "decision": d})

    def table(ps):
        out = ["| id | verdict | recommended | why | smallest edit | where |", "|---|---|---|---|---|---|"]
        for p in ps:
            r = rec.get(p["id"], {})
            recm = r.get("recommendation") or p["recommendation"]
            out.append(f"| {p['id']} | {p['verdict']} ({p['verdict_band']}) | {recm} | "
                       f"{(r.get('reason') or p['why'])[:160].replace('|', '/')} | "
                       f"{str(r.get('smallest_edit', ''))[:160].replace('|', '/')} | {p['where']} |")
        return "\n".join(out)

    if open_items:
        defaults = [x for x in rows if x["decision"] and x["decision"]["by"].startswith("default")]
        msg = [f"{len(open_items)} requirement(s) need YOUR decision (change_code / change_spec / skip):", "",
               table(open_items)]
        if defaults:
            msg += ["", f"{len(defaults)} confident code fix(es) default to change_code (override with Rnnn=skip):",
                    table(defaults)]
        msg += ["", "Answer with: wfx answer <run> -m \"R012=change_code R031=change_spec (why) rest=skip\""]
        print("\n".join(msg))
        open(f"{D}/pending.md", "w").write("\n".join(msg) + "\n")
        sys.exit(20)

    json.dump({"decisions": rows}, open(f"{D}/decisions.json", "w"), indent=1)
    acts = [x for x in rows if x["decision"]["decision"] in ("change_code", "change_spec")]
    print(f"decisions complete: {len(acts)} to change "
          f"({sum(1 for x in acts if x['decision']['decision'] == 'change_code')} code, "
          f"{sum(1 for x in acts if x['decision']['decision'] == 'change_spec')} spec), "
          f"{len(rows) - len(acts)} skipped")
    for x in rows:
        print(f"  {x['id']} {x['verdict']:18} -> {x['decision']['decision']:11} by {x['decision']['by']}"
              + (f" — {x['decision']['why']}" if x['decision']['why'] else ""))
    if not acts:
        sys.exit(78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"gate.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
