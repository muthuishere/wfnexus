#!/usr/bin/env python3
"""report.py — what the doctor did, what it did not, and what a person must do.

Always runs last. Moves `last_checked` forward, and remembers as `handled` the
findings that became a PR (so an open fix is not fixed again) and the
folder-level findings already reported as needing a person (so the same
incomplete folder is not re-diagnosed every hour). Failed runs are bounded by
`last_checked`, so they are never silenced — a new failure is a new finding.
"""
import json
import os
import subprocess


def load(name, default):
    try:
        return json.load(open(name)) or default
    except Exception:
        return default


def state(key, value=None):
    if value is None:
        r = subprocess.run(["wfx", "state", "get", "--workflow", key], capture_output=True, text=True)
        return r.stdout.strip()
    subprocess.run(["wfx", "state", "set", "--workflow", key, value], capture_output=True, text=True)


def main():
    # A collector that crashed has not looked, so "nothing found" would be a
    # lie: fail the run loudly instead of ending `done` (seen: a UnicodeDecodeError
    # in collect, and the run reported done with every step skipped).
    code = os.environ.get("COLLECT_EXIT", "")
    if code not in ("0", "3", "5"):
        print(f"collect did not finish (exit {code or 'unknown'}): nothing was checked — see the collect step's stderr")
        return 1
    found = load("findings.json", {"findings": []})
    diag = {d["finding"]: d for d in load("diagnosis.json", {}).get("diagnoses", [])}
    prs = {p["finding"]: p["url"] for p in load("prs.json", {}).get("prs", [])}
    merged = set(load("merged.json", {}).get("merged", []))
    blocked = {v["finding"]: v["reason"] for v in load("review.json", {}).get("verdicts", []) if v.get("verdict") == "block"}

    failed_proof = {r["finding"]: "; ".join(f"{c['check']}: {c['detail'][-300:]}" for c in r["checks"] if not c["ok"])
                    for r in load("verify.json", {}).get("results", []) if not r.get("ok")}
    if not found["findings"]:
        print("healthy: nothing broken since", found.get("since", "the last look"))
    person = []
    for f in found["findings"]:
        d = diag.get(f["id"], {})
        cause = d.get("cause") or ("needs_secret" if f.get("needs_secret") else f["kind"])
        what = f.get("workflow") or f.get("path")
        if f["id"] in merged:
            line = f"FIXED   {what} — merged {prs[f['id']]}"
        elif f["id"] in prs:
            line = f"PR      {what} — {prs[f['id']]} (awaiting merge)"
        elif f["id"] in blocked:
            line = f"BLOCKED {what} — reviewer: {blocked[f['id']][:200]}"
        elif f["id"] in failed_proof:
            line = f"UNPROVEN {what} — {failed_proof[f['id']][:200]}"
        else:
            line = f"OPEN    {what} — {cause}"
            if f.get("needs_secret"):
                cmds = " && ".join(f"wfx env set {s} --project {f.get('project')}" for s in f["needs_secret"])
                person.append(f"{what}: {cmds}")
            elif d.get("person_action"):
                person.append(f"{what}: {d['person_action']}")
            elif cause == "transient":
                person.append(f"{what}: re-run it (interrupted, not broken)")
        print(line + (f"  x{f['count']}" if f.get("count", 1) > 1 else ""))
    if person:
        print("\nneeds a person:")
        for p in person:
            print("  - " + p)

    if os.environ.get("DRY_RUN") == "true":
        print("\ndry run: last_checked and handled left as they were")
        return
    handled = set(filter(None, state("handled").split(",")))
    handled |= set(prs)
    handled |= {f["id"] for f in found["findings"]
                if f["kind"] in ("incomplete", "untracked", "load_problem") and f["id"] in diag and not diag[f["id"]].get("fix_now")}
    # what is still broken is carried to the next look, so moving last_checked
    # forward never drops an unfixed failure
    # what stopped this attempt — a failed proof or a reviewer's block — is the
    # next attempt's brief, carried with the finding
    failed_proof = {r["finding"]: "; ".join(f"{c['check']}: {c['detail'][-300:]}" for c in r["checks"] if not c["ok"])
                    for r in load("verify.json", {}).get("results", []) if not r.get("ok")}
    for f in found["findings"]:
        why = [x for x in (failed_proof.get(f["id"]) and "verify failed — " + failed_proof[f["id"]],
                           blocked.get(f["id"]) and "reviewer blocked — " + blocked[f["id"]]) if x]
        if why:
            # every attempt starts from a fresh branch, so earlier points must
            # stay in the brief or they come back; newest first, bounded
            earlier = f.get("previous_review", "")
            f["previous_review"] = (" | ".join(why) + (" || EARLIER ATTEMPTS: " + earlier if earlier else ""))[:6000]
    still = [f for f in found["findings"] if f["id"] not in prs and f["kind"] in ("run_failed", "transient")]
    state("open", json.dumps(still))
    if found.get("checked_at"):
        state("last_checked", found["checked_at"])
    state("handled", ",".join(sorted(handled)))


if __name__ == "__main__":
    import sys
    sys.exit(main())
