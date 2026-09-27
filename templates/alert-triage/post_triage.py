#!/usr/bin/env python3
"""post_triage.py — turn a judged triage into one comment per alert issue,
and into PROPOSED actions that a person must approve.

For each triaged issue:
  verdict_holds yes AND cause_supported yes
      -> post the triage comment (additive, reversible, marked `wfx-triage`)
      -> propose the action its verdict implies (actions.json) — never taken here:
           duplicate  close as not planned, "Duplicate of #N", + DUPLICATE_LABEL
           not_ours   close as not planned, naming the product it belongs to
           resolved   close as completed (the signal is gone now)
           noise      close as not planned
           real       nothing to close; + TRIAGED_LABEL if one is configured
  anything uncertain, a classifier failure, or a verdict judged WRONG
      -> no comment; the issue goes to a person (uncertain.json). The band may
         route work to a person; it never overrules one.

DRY_RUN prints the comments and actions it would make and writes nothing.
Env: ISSUE_REPO, ALERT_LABEL, DUPLICATE_LABEL (duplicate), TRIAGED_LABEL (""),
WORKFLOW_NAME, DRY_RUN.
"""
import json
import os
import subprocess
import sys

REPO = os.environ["ISSUE_REPO"]
DUP_LABEL = os.environ.get("DUPLICATE_LABEL", "duplicate")
TRIAGED = os.environ.get("TRIAGED_LABEL", "")
WORKFLOW = os.environ.get("WORKFLOW_NAME", "alert-triage")
DRY = os.environ.get("DRY_RUN", "").lower() in ("1", "true", "yes")


def gh(*args):
    r = subprocess.run(["gh", *args], capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"gh {' '.join(args[:2])} failed: {r.stderr.strip()[:300]}")
    return r.stdout


triage = json.load(open("triage.json"))
verdicts = {}
if os.path.exists("verdicts.jsonl"):
    for line in open("verdicts.jsonl"):
        if line.strip():
            v = json.loads(line)
            verdicts[str(v["id"])] = v
targets = {t["number"] for t in json.load(open("targets.json"))}

commented, actions, uncertain, report = [], [], [], []


def comment_body(t, a):
    vh, cs = a["verdict_holds"], a["cause_supported"]
    lines = [f"### Triage: **{t['verdict'].replace('_', ' ')}** — {t['summary']}", ""]
    if t["verdict"] == "duplicate" and t.get("duplicate_of"):
        lines += [f"**Duplicate of:** #{t['duplicate_of']}", ""]
    lines += [f"**Cause:** {t['cause']}", "", "**Evidence:**", *[f"- {e}" for e in t["evidence"][:10]]]
    if t.get("code_refs"):
        lines += ["", "**Where in the code:** " + ", ".join(f"`{r}`" for r in t["code_refs"][:8])]
    lines += ["", f"**Fix:** {t['fix']}", f"**Owner:** {t['owner']}", f"**Still happening:** {t['still_happening']}"]
    if t.get("related"):
        lines += [f"**Related:** " + ", ".join(f"#{n}" for n in t["related"])]
    prop = proposed(t)
    if prop:
        lines += ["", "**Proposed (waits for a person's approval):** " + "; ".join(p["what"] for p in prop)]
    lines += ["", f"<sub>{WORKFLOW} · classifier: verdict holds {vh['noul']:.2f} ({vh['band']}), "
                  f"cause supported {cs['noul']:.2f} ({cs['band']}). Read-only evidence; verify before acting.</sub>",
              f"<!-- wfx-triage: {t['verdict']} -->"]
    return "\n".join(lines)


def proposed(t):
    n, v = t["number"], t["verdict"]
    if v == "duplicate" and t.get("duplicate_of"):
        return [{"issue": n, "kind": "close", "reason": "not planned", "label": DUP_LABEL,
                 "comment": f"Duplicate of #{t['duplicate_of']}.", "what": f"close #{n} as a duplicate of #{t['duplicate_of']}"}]
    if v == "not_ours":
        return [{"issue": n, "kind": "close", "reason": "not planned", "label": "",
                 "comment": f"Not this product's: {t['owner']}.", "what": f"close #{n} — belongs to {t['owner']}"}]
    if v == "resolved":
        return [{"issue": n, "kind": "close", "reason": "completed", "label": "",
                 "comment": "The signal is no longer raised; closing as resolved.", "what": f"close #{n} as resolved"}]
    if v == "noise":
        return [{"issue": n, "kind": "close", "reason": "not planned", "label": "",
                 "comment": "Not an actionable problem (see triage).", "what": f"close #{n} as noise"}]
    if v == "real" and TRIAGED:
        return [{"issue": n, "kind": "label", "label": TRIAGED, "what": f"label #{n} {TRIAGED}"}]
    return []


for t in triage["issues"]:
    n = t["number"]
    if n not in targets:
        report.append(f"#{n}: ignored — not an issue this run gathered")
        continue
    v = verdicts.get(str(n))
    if not v or v.get("error"):
        uncertain.append({**t, "why": "classifier could not judge it: " + ((v or {}).get("error") or "no verdict")})
        continue
    a = v["answers"]
    bands = (a["verdict_holds"]["band"], a["cause_supported"]["band"])
    if bands != ("yes", "yes"):
        uncertain.append({**t, "why": f"verdict_holds={a['verdict_holds']['noul']:.2f} ({bands[0]}), "
                                      f"cause_supported={a['cause_supported']['noul']:.2f} ({bands[1]})"})
        continue
    body = comment_body(t, a)
    if DRY:
        print(f"----- (dry run) would comment on #{n} -----\n{body}\n")
    else:
        gh("issue", "comment", str(n), "-R", REPO, "--body", body)
    commented.append(n)
    actions += proposed(t)
    report.append(f"#{n}: {t['verdict']}" + (f" of #{t['duplicate_of']}" if t.get("duplicate_of") else "")
                  + f" — {t['summary'][:120]}")

for u in uncertain:
    report.append(f"#{u['number']}: needs a person — {u['why']}")
summary = {"dry_run": DRY, "commented": commented, "proposed_actions": [p["what"] for p in actions],
           "needs_a_person": [u["number"] for u in uncertain], "report": report}
print(json.dumps(summary, indent=1))
if DRY:
    # A dry run never opens a gate and never waits on a person.
    for f in ("actions.json", "uncertain.json"):
        if os.path.exists(f):
            os.remove(f)
    sys.exit(0)
if actions:
    json.dump(actions, open("actions.json", "w"), indent=1)
if uncertain:
    json.dump(uncertain, open("uncertain.json", "w"), indent=1)
sys.exit(0)
