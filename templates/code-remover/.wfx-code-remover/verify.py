#!/usr/bin/env python3
"""verify.py — the removals, proven by something other than the agent that made them.

  * the suite again, same ruler as the baseline: build, vet, tests counted;
  * the test count may only drop by tests the remover NAMED as orphans;
  * every commit on the branch is one planned removal ("remove <symbol>: …"),
    and nothing was committed that the plan did not approve;
  * a removal commit that ADDS many lines is fix-forward, which safe-remover
    forbids — flagged;
  * the finders again: each removed symbol is gone, and anything that became
    unreachable BECAUSE of these removals is listed for the next run.

Writes verify.json. Exit 0 green, 70 when the branch is red or unplanned
commits exist (the run stops; nothing is published).
"""
import json
import os
import re
import subprocess
import sys

D = ".wfx-code-remover"


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout


def main():
    base = json.load(open(f"{D}/baseline.json"))
    plan = json.load(open(f"{D}/plan.json"))["plan"]
    approved = {r["symbol"]: r for r in plan if r["verdict"] == "remove"}
    removals = []
    if os.path.exists(f"{D}/removals.jsonl"):
        removals = [json.loads(l) for l in open(f"{D}/removals.jsonl") if l.strip()]

    s = subprocess.run([sys.executable, f"{D}/suite.py", "--scope", base["scope"], "--out", f"{D}/after.json"],
                       capture_output=True, text=True)
    after = json.load(open(f"{D}/after.json"))
    problems = []
    if not after["ok"]:
        problems.append("the suite is RED after the removals")

    commits = []
    for line in git("log", "--reverse", "--format=%H%x09%s", f"{base['base']}..HEAD").splitlines():
        sha, subj = line.split("\t", 1)
        stat = git("show", "--numstat", "--format=", sha)
        add = dele = 0
        files = []
        for st in stat.splitlines():
            parts = st.split("\t")
            if len(parts) == 3:
                a, d, f = parts
                add += int(a) if a.isdigit() else 0
                dele += int(d) if d.isdigit() else 0
                files.append(f)
        m = re.match(r"remove (\S+?):", subj)
        sym = m[1] if m else None
        planned = sym in approved or any(sym and (sym == k.split(".")[-1] or k.endswith("." + sym)) for k in approved)
        commits.append({"sha": sha[:10], "subject": subj, "symbol": sym, "planned": planned,
                        "added": add, "deleted": dele, "files": files})
        if not planned:
            problems.append(f"commit {sha[:10]} is not a planned removal: {subj}")
        if add > 10 and add > dele / 2:
            problems.append(f"commit {sha[:10]} adds {add} lines — a removal that writes code is a fix-forward")

    before_t = base["totals"]
    orphans = sum(len(r.get("orphan_tests") or []) for r in removals if r.get("status") == "removed")
    dropped = (before_t.get("pass") or 0) - (after["totals"].get("pass") or 0)
    if dropped > 0 and dropped > orphans:
        # subtests make exact accounting imperfect; an unexplained drop is still a finding
        problems.append(f"{dropped} fewer passing tests, but only {orphans} were named as orphans")

    # the finders again, on the result
    inv = subprocess.run([sys.executable, f"{D}/inventory.py"], capture_output=True, text=True,
                         env={**os.environ, "SCOPE": base["scope"], "CANDIDATES_OUT": f"{D}/candidates.after.jsonl"})
    now = {}
    if os.path.exists(f"{D}/candidates.after.jsonl"):
        now = {json.loads(l)["symbol"]: json.loads(l) for l in open(f"{D}/candidates.after.jsonl") if l.strip()}
    before_syms = {r["symbol"] for r in plan}
    committed = {c["symbol"] for c in commits if c["planned"]}
    removed = [s for s in approved if s in committed or s.split(".")[-1] in committed]
    still_there = [s for s in removed if s in now]
    if still_there:
        problems.append("committed as removed but the finders still report: " + ", ".join(still_there))
    cascade = sorted(s for s in now if s not in before_syms)

    lines_deleted = sum(c["deleted"] for c in commits)
    result = {"ok": not problems, "problems": problems, "before": before_t, "after": after["totals"],
              "suite_after_ok": after["ok"], "commits": commits, "lines_deleted": lines_deleted,
              "lines_added": sum(c["added"] for c in commits), "orphan_tests_named": orphans,
              "newly_unreachable": cascade, "still_reported": still_there,
              "reverted": [r for r in removals if r.get("status") == "reverted"]}
    json.dump(result, open(f"{D}/verify.json", "w"), indent=1)

    print(s.stdout.rstrip())
    print(f"before: {before_t} · after: {after['totals']} · orphan tests named: {orphans}")
    print(f"{len(commits)} commits on {base['branch']}, −{lines_deleted} / +{result['lines_added']} lines")
    for c in commits:
        print(f"  {c['sha']} {'ok ' if c['planned'] else 'UNPLANNED'} −{c['deleted']}/+{c['added']}  {c['subject'][:90]}")
    for r in result["reverted"]:
        print(f"  reverted: {r.get('symbol')} — {str(r.get('error', ''))[:120]}")
    if cascade:
        print("newly unreachable after these removals (candidates for the next run): " + ", ".join(cascade))
    for p in problems:
        print("PROBLEM:", p)
    if not commits:
        print("PROBLEM: no removal commit was made")
        sys.exit(70)
    sys.exit(0 if not problems else 70)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"verify.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
