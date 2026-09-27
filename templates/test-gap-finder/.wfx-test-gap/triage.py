#!/usr/bin/env python3
"""triage.py — makes_sense / does_not_make_sense / unsure, by a decision table.

For every GAP scenario (and every `unaddressed` one), the calibrated classifier
(`wfx judge`, JEV) answers triage-questions.yaml over evidence this script
builds from the MACHINE's facts — the source lines at the evidence, whether any
test ever executed that line — plus the analyst's description. Then, literally:

    unaddressed (no analyst accounted for it)                     -> unsure
    the classifier returned nothing                               -> unsure
    verdict=makes_sense, regression_value not `no`, and either the choice is
      `sure` or p(makes_sense) >= 0.6 with regression_value `yes`   -> makes_sense
    verdict=does_not_make_sense, regression_value not `yes`, and either
      `sure` or p(does_not_make_sense) >= 0.6 with regression `no`  -> does_not_make_sense
    anything else                                                 -> unsure

A person then decides every `unsure` one (and may overrule any other): their
reply, from the run's needs-input answer, is read from human.txt:

    approve F3-S02 F3-S05 F9-U01
    reject F2-S04 F2-S07: covered by the README example test, not worth a unit test

Until every `unsure` row has a human decision this exits 20 (the workflow
parks for a person). Approved rows are capped at MAX_TESTS, riskiest first.
Writes triage.json (every row, with who decided and why) and plan.md.

Exit 0 = a plan with tests to write; 20 = a person must decide; 78 = nothing
to write; 70 = error.
"""
import json
import os
import re
import subprocess
import sys

D = ".wfx-test-gap"
MAX = int(os.environ.get("MAX_TESTS") or 20)
PRIORITY = ["spec_promise", "error", "boundary", "null", "async", "doc_promise", "untested_public", "edge", "combination",
            "concurrency"]


def snippet(ev, around=4):
    m = re.match(r"^`?([^:`]+):(\d+)", (ev or "").strip())
    if not m or not os.path.isfile(m[1]):
        return ""
    lines = open(m[1], errors="replace").read().splitlines()
    n = int(m[2])
    lo, hi = max(1, n - around), min(len(lines), n + around)
    return "\n".join(f"{i:>4}{'>' if i == n else ' '} {lines[i - 1]}" for i in range(lo, hi + 1))


def prob(answer, option):
    return float((answer.get("probabilities") or {}).get(option) or 0.0)


def one_line(t, n):
    t = " ".join(str(t or "").split())
    return t if len(t) <= n else t[:n - 1] + "…"


def test_source(where):
    """The source of the test a `covered` claim names: its def/it block, ~40 lines."""
    path, _, rest = (where or "").partition("::")
    leaf = re.split(r"::| > ", where or "")[-1].strip()
    files = [path] if path and os.path.isfile(path) else []
    if not files:
        style = json.load(open(f"{D}/style.json"))
        for t in style.get("test_dirs") or []:
            for dp, dn, fn in os.walk(t):
                dn[:] = [d for d in dn if d != "node_modules" and not d.startswith(".")]
                files += [os.path.join(dp, f) for f in fn]
    for f in files:
        try:
            lines = open(f, errors="replace").read().splitlines()
        except OSError:
            continue
        for i, l in enumerate(lines):
            if re.search(r"def\s+" + re.escape(leaf) + r"\b|func\s+" + re.escape(leaf) + r"\b|['\"`]" + re.escape(leaf[:60]), l):
                return f"{f}:{i + 1}\n" + "\n".join(lines[i:i + 40])
    return ""


def verify_covered(scen):
    """Ask JEV whether each `covered` claim's test really asserts the scenario; `no` -> back to gap."""
    cov = [s for s in scen if s["status"] == "covered"]
    cache = {}
    if os.path.exists(f"{D}/verdicts-covered.jsonl"):
        cache = {json.loads(l)["id"]: json.loads(l) for l in open(f"{D}/verdicts-covered.jsonl") if l.strip()}
    ask = []
    for s in cov:
        if s["id"] in cache:
            continue
        src = test_source(s.get("covered_by", ""))
        if not src:
            continue
        ask.append({"id": s["id"], "state": f"Scenario: {s['title']}\nGiven/when/then: {s.get('given_when_then', '')}\n"
                                             f"Expected: {s.get('expected', '')}\nClaimed covering test {s['covered_by']}:\n{src}"})
    if ask:
        with open(f"{D}/judge-covered.jsonl", "w") as f:
            for a in ask:
                f.write(json.dumps(a) + "\n")
        j = subprocess.run(["wfx", "judge", "-q", f"{D}/covered-questions.yaml", "--items", f"{D}/judge-covered.jsonl"],
                           capture_output=True, text=True, timeout=900)
        if j.returncode:
            print("the classifier could not check the covered claims: " + j.stderr.strip()[:400])
            sys.exit(70)
        with open(f"{D}/verdicts-covered.jsonl", "a") as f:
            for l in j.stdout.splitlines():
                if l.strip():
                    cache[json.loads(l)["id"]] = json.loads(l)
                    f.write(l + "\n")
    demoted = 0
    for s in cov:
        p = ((cache.get(s["id"]) or {}).get("answers") or {}).get("pins") or {}
        s["pins"] = p.get("noul")
        if p.get("band") == "no":
            s["status"] = "gap"
            s["demoted"] = f"claimed covered by {s['covered_by']}, but that test does not assert it (JEV pins={p.get('noul')})"
            demoted += 1
    return demoted


def human_decisions():
    txt = open(f"{D}/human.txt").read() if os.path.exists(f"{D}/human.txt") else ""
    out = {}
    for line in txt.splitlines():
        m = re.match(r"^\s*[-*]?\s*(approve|approved|reject|rejected)\b[:\s]*(.*)$", line, re.I)
        if not m:
            continue
        verb = "approve" if m[1].lower().startswith("approve") else "reject"
        rest = m[2]
        ids = re.findall(r"\bF\d+-[SU]\d+\b", rest)
        reason = re.split(r"\bF\d+-[SU]\d+\b", rest)[-1].strip(" :—-,;") if ids else ""
        for i in ids:
            out[i] = (verb, reason)
        # `approve remaining` / `reject remaining: why` decides every row still unsure
        # after the explicit ids — a person's call, recorded with their reason
        m2 = re.match(r"^(?:all\s+)?(?:the\s+)?(remaining|rest|others)\b[:\s—-]*(.*)$", rest.strip(), re.I)
        if not ids and m2:
            out["*"] = (verb, m2[2].strip())
    return out


def main():
    scen = [json.loads(l) for l in open(f"{D}/scenarios.jsonl") if l.strip()]
    demoted = verify_covered(scen)
    if demoted:
        print(f"{demoted} `covered` claim(s) sent back as gaps — their test does not assert the scenario")
    todo = [s for s in scen if s["status"] in ("gap", "unaddressed")]
    cov = {}
    if os.path.exists(f"{D}/before.json"):
        cov = json.load(open(f"{D}/before.json")).get("coverage", {}).get("files", {})

    def never_run(s):
        m = re.match(r"^`?([^:`]+):(\d+)", s.get("evidence", ""))
        if not m or m[1] not in cov:
            return None
        return int(m[2]) in set(cov[m[1]].get("missing") or [])

    # cached verdicts: a re-run after a person answers must not re-ask (or re-bill) the classifier
    cache = {}
    if os.path.exists(f"{D}/verdicts.jsonl"):
        cache = {json.loads(l)["id"]: json.loads(l) for l in open(f"{D}/verdicts.jsonl") if l.strip()}
    ask = []
    for s in todo:
        if s["status"] == "unaddressed" or s["id"] in cache:
            continue
        nr = never_run(s)
        state = "\n".join([
            f"Scenario {s['id']} ({s['kind']}) in {s['file']}: {s['title']}",
            f"Given/when/then: {s.get('given_when_then', '')}",
            f"What the code or its docs promise: {s.get('expected', '')}",
            f"Evidence: {s.get('evidence', '')}" + ("" if nr is None else
                ("; NO existing test ever executes this line (coverage)" if nr else "; the existing suite does execute this line, but no test asserts this scenario")),
            (f"Note: {s['demoted']}" if s.get("demoted") else ""),
            "Source around the evidence:", snippet(s.get("evidence", ""))])
        ask.append({"id": s["id"], "state": state})
    if ask:
        with open(f"{D}/judge-items.jsonl", "w") as f:
            for a in ask:
                f.write(json.dumps(a) + "\n")
        j = subprocess.run(["wfx", "judge", "-q", f"{D}/triage-questions.yaml", "--items", f"{D}/judge-items.jsonl"],
                           capture_output=True, text=True, timeout=900)
        if j.returncode:
            print("the classifier could not judge the scenarios: " + j.stderr.strip()[:600])
            print("(it needs TYPESAFE_API_KEY or OPENROUTER_API_KEY in the env store)")
            sys.exit(70)
        with open(f"{D}/verdicts.jsonl", "a") as f:
            for l in j.stdout.splitlines():
                if l.strip():
                    v = json.loads(l)
                    cache[v["id"]] = v
                    f.write(l + "\n")

    human = human_decisions()
    rows = []
    for s in todo:
        v = cache.get(s["id"], {})
        a = v.get("answers") or {}
        vd, rv = a.get("verdict", {}), a.get("regression_value", {})
        row = {"id": s["id"], "file": s["file"], "kind": s["kind"], "title": s["title"],
               "given_when_then": s.get("given_when_then", ""), "expected": s.get("expected", ""),
               "evidence": s.get("evidence", ""), "items": s.get("items", []), "never_run": never_run(s),
               "classifier": {"verdict": vd.get("choice"), "band": vd.get("band"), "confidence": vd.get("confidence"),
                              "regression_value": rv.get("noul"), "regression_band": rv.get("band")}}
        if s["status"] == "unaddressed":
            row.update(machine="unsure", why="no analyst accounted for this surface item; a person decides whether it matters")
        elif v.get("error") or not a:
            row.update(machine="unsure", why="the classifier returned no answer: " + str(v.get("error", "missing")))
        elif vd.get("choice") == "makes_sense" and rv.get("band") != "no" and (
                vd.get("band") == "sure" or (prob(vd, "makes_sense") >= 0.6 and rv.get("band") == "yes")):
            row.update(machine="makes_sense", why=f"classifier: makes_sense ({vd.get('band')}, p={prob(vd, 'makes_sense'):.2f}); "
                                                  f"regression value {rv.get('noul'):.2f} ({rv.get('band')})")
        elif vd.get("choice") == "does_not_make_sense" and rv.get("band") != "yes" and (
                vd.get("band") == "sure" or (prob(vd, "does_not_make_sense") >= 0.6 and rv.get("band") == "no")):
            row.update(machine="does_not_make_sense", why=f"classifier: does_not_make_sense ({vd.get('band')}, p={prob(vd, 'does_not_make_sense'):.2f}); "
                                                          f"regression value {rv.get('noul'):.2f} ({rv.get('band')})")
        else:
            row.update(machine="unsure", why=f"classifier: {vd.get('choice')} ({vd.get('band')}, "
                                             f"{(vd.get('confidence') or 0):.2f}); regression value "
                                             f"{(rv.get('noul') or 0):.2f} ({rv.get('band')})")
        row["verdict"], row["decided_by"] = row["machine"], "classifier"
        if s["id"] in human or (row["machine"] == "unsure" and "*" in human):
            verb, reason = human.get(s["id"]) or human["*"]
            row["verdict"] = "makes_sense" if verb == "approve" else "does_not_make_sense"
            row["decided_by"] = "human"
            row["human"] = ("approved" if verb == "approve" else "rejected") + (f": {reason}" if reason else "")
        rows.append(row)

    pending = [r for r in rows if r["verdict"] == "unsure"]
    approved = sorted([r for r in rows if r["verdict"] == "makes_sense"],
                      # a person's explicit approval ranks first; then lines no test ever ran; then the riskiest kinds
                      key=lambda r: (r["decided_by"] != "human", r["never_run"] is not True,
                                     PRIORITY.index(r["kind"]) if r["kind"] in PRIORITY else 99, r["id"]))
    for r in approved[MAX:]:
        r.update(verdict="deferred", why=r["why"] + f"; over this run's cap of {MAX} tests — next run")
    approved = approved[:MAX]
    covered = [s for s in scen if s["status"] == "covered"]
    json.dump({"max_tests": MAX, "rows": rows, "covered": covered}, open(f"{D}/triage.json", "w"), indent=1)

    counts = {}
    for r in rows:
        counts[r["verdict"]] = counts.get(r["verdict"], 0) + 1
    md = [f"# Test-gap plan — {len(scen)} scenarios: {len(covered)} already covered, "
          + ", ".join(f"{n} {k}" for k, n in sorted(counts.items())), "",
          "| verdict | by | id | kind | scenario | evidence | why |", "|---|---|---|---|---|---|---|"]
    order = {"makes_sense": 0, "unsure": 1, "does_not_make_sense": 2, "deferred": 3}
    for r in sorted(rows, key=lambda r: (order.get(r["verdict"], 9), r["id"])):
        md.append(f"| **{r['verdict']}** | {r['decided_by']} | {r['id']} | {r['kind']} | "
                  f"{one_line(r['title'], 90).replace('|', '/')} | {r['evidence']} | "
                  f"{(r.get('human') or r['why']).replace('|', '/')[:140]} |")
    open(f"{D}/plan.md", "w").write("\n".join(md) + "\n")

    if pending:
        print(f"{len(pending)} scenario(s) need a person — the classifier is not sure a test should pin them.")
        print("Reply with lines like:  approve F3-S02 F3-S05   /   reject F2-S04: why   /   reject remaining: why")
        print(f"(at most {MAX} tests are written per run: your approvals first, then the classifier's, riskiest first)\n")
        for r in pending:
            print(f"  {r['id']} [{r['kind']}] {one_line(r['title'], 110)} ({r['evidence']})")
            print(f"      {one_line(r['given_when_then'], 160)} → {one_line(r['expected'], 120)} · {r['why']}")
        dns = [r for r in rows if r["verdict"] == "does_not_make_sense"]
        ms = [r for r in rows if r["verdict"] == "makes_sense"]
        print(f"\nAlready decided: {len(ms)} makes_sense, {len(dns)} does_not_make_sense (overrule any with approve/reject).")
        sys.exit(20)
    print("\n".join(md))
    if not approved:
        print("\nNo scenario is worth a test this run — no branch commits, no PR.")
        sys.exit(78)
    print(f"\nApproving the next step writes {len(approved)} test(s) for the makes_sense rows above, on this run's branch only.")


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"triage.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
