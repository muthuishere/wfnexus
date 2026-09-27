#!/usr/bin/env python3
"""plan.py — usage-prover's decision table, applied LITERALLY, by code.

Inputs: candidates.jsonl + checks.jsonl (machine checks) and prove.json (the
prover agent's reading of every hit). It:

  1. refuses to go on if the prover touched the repository (it may only read);
  2. refuses a prover that ignored evidence: every call-shaped hit the machine
     found must be either a real reference or dismissed with a reason — an
     unexplained hit makes the candidate `needs-human`, whatever the model says;
  3. writes evidence.jsonl in plain sentences built from the MACHINE's checks
     plus the prover's reading, and asks the calibrated classifier (wfx judge);
  4. applies the table:
        kind=bug (any band) or the prover saw a lost caller   -> bug (never deleted)
        a real reference or text that depends on it           -> keep (cites it)
        >=3 checks, unused=yes, kind=remove (sure)            -> remove
        anything else                                         -> needs-human
     The classifier never overrules a reference, and never promotes to remove
     on its own;
  5. caps the plan at MAX_REMOVALS (test-only and unexported first — the
     lowest-risk removals), and writes plan.json + plan.md.

Exit 0 with removals, 78 when nothing is safe to remove (the run ends with a
report, not a PR), 70 on an error.
"""
import json
import os
import re
import subprocess
import sys

D = ".wfx-code-remover"
MAX = int(os.environ.get("MAX_REMOVALS") or 10)


def loc(h):
    m = re.match(r"^([^:]+):(\d+)", h.strip().strip("`"))
    return f"{m[1]}:{m[2]}" if m else h.strip()


def main():
    dirty = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"],
                           capture_output=True, text=True).stdout.strip()
    if dirty:
        print("the prover modified tracked files — it may only read. Refusing to plan on a changed tree:\n" + dirty)
        sys.exit(70)
    cands = {json.loads(l)["id"]: json.loads(l) for l in open(f"{D}/candidates.jsonl") if l.strip()}
    checks = {json.loads(l)["id"]: json.loads(l) for l in open(f"{D}/checks.jsonl") if l.strip()}
    prove = json.load(open(f"{D}/prove.json"))
    readings = {p["id"]: p for p in prove.get("candidates", [])}

    items = []
    for cid, c in cands.items():
        k, p = checks[cid], readings.get(cid)
        if p is None:
            continue
        lines = [f"Candidate {c['symbol']} ({c['kind']}) at {c['file']}:{c['line']}, found by {c['tool']}: {c['evidence']}."]
        lit = k["checks"][0]
        lines.append(f"Check 1, literal search of every tracked file (git grep -w {c['name']}): "
                     f"{lit['code_hits']} code hits and {lit['text_hits']} text hits besides the definition and its own doc comment.")
        lines.append(f"Check 2, call and selector shapes: {k['checks'][1]['hits']} hits.")
        lines.append("Check 3, indirect use: " + ("; ".join(k["indirect"]) if k["indirect"] else "no interface method, reflection, linkname or build constraint found") + ".")
        if k["history"]:
            lines.append(f"History (git log -S): {'; '.join(k['history'][:3])}.")
        g = [x for x in k["checks"] if x["check"] == "graph"]
        if g:
            lines.append(f"Check 4, code graph callers: {len(g[0]['callers'])}.")
        rr, dep, desc = p.get("real_references", []), p.get("dependent_text", []), p.get("descriptive_text", [])
        lines.append("Every hit read by the prover: " + (
            f"REAL references: {'; '.join(rr)}." if rr else "none of the hits is a real reference to this symbol."))
        if p.get("dismissed_hits"):
            lines.append("Dismissed hits: " + "; ".join(f"{d['hit']} ({d['why']})" for d in p["dismissed_hits"][:8]) + ".")
        if dep:
            lines.append(f"Text that DEPENDS on it by name: {'; '.join(dep)}.")
        if desc:
            lines.append(f"Text that only describes or quotes it (to be updated with the removal): {'; '.join(desc)}.")
        if p.get("lost_caller"):
            lines.append(f"Possible LOST CALLER: {p.get('lost_caller_why', '')}.")
        lines.append(f"Prover's reading: {p.get('analysis', '')}")
        items.append({"id": cid, "state": "\n".join(lines)})
    with open(f"{D}/evidence.jsonl", "w") as f:
        for it in items:
            f.write(json.dumps(it) + "\n")

    j = subprocess.run(["wfx", "judge", "-q", f"{D}/removal-questions.yaml", "--items", f"{D}/evidence.jsonl"],
                       capture_output=True, text=True, timeout=600)
    if j.returncode:
        print("the classifier could not judge the candidates: " + j.stderr.strip()[:600])
        print("(it needs TYPESAFE_API_KEY or OPENROUTER_API_KEY in the env store: wfx env set TYPESAFE_API_KEY)")
        sys.exit(70)
    verdicts = {json.loads(l)["id"]: json.loads(l) for l in j.stdout.splitlines() if l.strip()}

    plan = []
    for cid, c in cands.items():
        k, p, v = checks[cid], readings.get(cid), verdicts.get(cid, {})
        row = {"id": cid, "symbol": c["symbol"], "file": c["file"], "line": c["line"], "kind": c["kind"],
               "tool": c["tool"], "evidence": c["evidence"], "testOnly": c.get("testOnly", False),
               "module": c.get("module"), "checks_run": k["checks_run"]}
        if p is None:
            plan.append({**row, "verdict": "needs-human", "why": "the prover did not read this candidate"})
            continue
        a = v.get("answers") or {}
        un, kind = a.get("unused", {}), a.get("kind", {})
        row.update({"unused": un.get("noul"), "unused_band": un.get("band"), "kind_choice": kind.get("choice"),
                    "kind_band": kind.get("band"), "kind_confidence": kind.get("confidence"),
                    "descriptive_text": p.get("descriptive_text", []), "analysis": p.get("analysis", "")})
        accounted = {loc(x) for x in p.get("real_references", [])} | {loc(d["hit"]) for d in p.get("dismissed_hits", [])}
        unexplained = [h for h in k["call_hits"] if loc(h) not in accounted]
        if v.get("error") or not a:
            row.update(verdict="needs-human", why="the classifier returned no answer: " + str(v.get("error", "missing")))
        elif kind.get("choice") == "bug" or p.get("lost_caller"):
            row.update(verdict="bug", why=p.get("lost_caller_why") or "the classifier reads it as a lost caller")
        elif p.get("real_references") or p.get("dependent_text"):
            row.update(verdict="keep", why="; ".join((p.get("real_references") or []) + (p.get("dependent_text") or []))[:400])
        elif unexplained:
            row.update(verdict="needs-human", why="call-shaped hits the prover did not account for: " + "; ".join(unexplained[:3]))
        elif k["checks_run"] >= 3 and un.get("band") == "yes" and kind.get("choice") == "remove" and kind.get("band") == "sure":
            row.update(verdict="remove", why=p.get("analysis", "")[:400])
        else:
            row.update(verdict="needs-human",
                       why=f"unused={un.get('noul')} ({un.get('band')}), kind={kind.get('choice')} ({kind.get('band')})")
        plan.append(row)

    removes = sorted([r for r in plan if r["verdict"] == "remove"],
                     key=lambda r: (not r["testOnly"], r["symbol"].split(".")[-1][:1].isupper(), r["file"], r["line"]))
    for r in removes[MAX:]:
        r.update(verdict="deferred", why=f"over this run's cap of {MAX} removals — next run")
    json.dump({"max_removals": MAX, "plan": plan}, open(f"{D}/plan.json", "w"), indent=1)
    with open(f"{D}/verdicts.jsonl", "w") as f:
        for r in plan:
            f.write(json.dumps(r) + "\n")

    counts = {}
    for r in plan:
        counts[r["verdict"]] = counts.get(r["verdict"], 0) + 1
    md = [f"# Removal plan — {len(plan)} candidates: " + ", ".join(f"{n} {k}" for k, n in sorted(counts.items())), "",
          "| verdict | symbol | where | unused (JEV) | kind (JEV) | why |", "|---|---|---|---|---|---|"]
    order = {"remove": 0, "bug": 1, "needs-human": 2, "keep": 3, "deferred": 4}
    for r in sorted(plan, key=lambda r: order.get(r["verdict"], 9)):
        md.append(f"| **{r['verdict']}** | `{r['symbol']}` | {r['file']}:{r['line']} | "
                  f"{r.get('unused')} {r.get('unused_band') or ''} | {r.get('kind_choice') or ''} {r.get('kind_band') or ''} | "
                  f"{(r.get('why') or '').replace('|', '/').replace(chr(10), ' ')[:140]} |")
    open(f"{D}/plan.md", "w").write("\n".join(md) + "\n")
    print("\n".join(md))
    if counts.get("remove", 0) == 0:
        print("\nNothing is provably safe to remove this run — no branch commits, no PR.")
        sys.exit(78)
    print(f"\nApproving the next step deletes the {counts['remove']} `remove` rows above, one commit each, "
          f"on this run's branch only. Nothing is pushed.")


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"plan.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
