#!/usr/bin/env python3
"""file_issues.py — judged findings -> GitHub issues, one per problem, no spam.

Reads signals.json (what the rules raised), triage.json (the agent's
explanation), verdicts.jsonl and dup_verdicts.jsonl (the classifier's bands).

- ONE issue per fingerprint. A fingerprint is found again in an open issue's
  body OR its comments (`<!-- wfx-fingerprint: … -->`), so a finding the agent
  mapped onto an issue a person filed by hand is recognised next time too.
- A finding the agent says is the same problem as an existing issue is merged
  into it only when the classifier agrees (`same_problem` yes). Uncertain goes
  to a person; no files a new one. (The #415/#417 duplicate.)
- A problem that was reported, closed, and is back gets a new issue that links
  the closed one — a regression is not a fresh surprise.
- An open issue is commented on at most once per COMMENT_EVERY_H hours.
- EVERY candidate the rules raised must be explained by some finding
  (its fingerprint or `covers`). One the agent dropped is not silently lost:
  it goes to a person.
- A classifier outage is not "nothing to file": an item with no verdict is
  uncertain and goes to a person.
- `in_scope: false` (another product on shared infrastructure) is never filed.

Exit 3 when a person must decide (uncertain.json lists what). In DRY_RUN
nothing is written to GitHub and the run never waits on a person: it prints
what it WOULD file, comment and ask.

Env: ISSUE_REPO, ISSUE_LABEL, PRODUCT, WORKFLOW_NAME, DRY_RUN, COMMENT_EVERY_H (24).
"""
import datetime as dt
import json
import os
import re
import subprocess
import sys

REPO = os.environ["ISSUE_REPO"]
LABEL = os.environ.get("ISSUE_LABEL", "sre-alert")
PRODUCT = os.environ.get("PRODUCT", "this product")
WORKFLOW = os.environ.get("WORKFLOW_NAME", "prod-watch")
DRY = os.environ.get("DRY_RUN", "").lower() in ("1", "true", "yes")
EVERY_H = float(os.environ.get("COMMENT_EVERY_H") or 24)
MARK = "wfx-fingerprint:"
MARK_RX = re.compile(r"wfx-fingerprint:\s*([0-9a-f]{6,40})")


def gh(*args, input=None):
    r = subprocess.run(["gh", *args], capture_output=True, text=True, input=input)
    if r.returncode != 0:
        raise SystemExit(f"gh {' '.join(args[:2])} failed: {r.stderr.strip()[:300]}")
    return r.stdout


def load_jsonl(path):
    out = {}
    if os.path.exists(path):
        for line in open(path):
            if line.strip():
                v = json.loads(line)
                out[v["id"]] = v
    return out


signals = json.load(open("signals.json"))
triage = json.load(open("triage.json"))
verdicts = load_jsonl("verdicts.jsonl")
dups = load_jsonl("dup_verdicts.jsonl")

issues = json.loads(gh("issue", "list", "-R", REPO, "--label", LABEL, "--state", "all", "--limit", "300",
                       "--json", "number,title,body,comments,createdAt,state"))
open_by_fp, closed_by_fp, open_by_no = {}, {}, {}
for i in issues:
    texts = [i.get("body") or ""] + [c.get("body") or "" for c in i.get("comments") or []]
    for t in texts:
        for f in MARK_RX.findall(t):
            (open_by_fp if i["state"] == "OPEN" else closed_by_fp).setdefault(f, i)
    if i["state"] == "OPEN":
        open_by_no[i["number"]] = i

now = dt.datetime.now(dt.timezone.utc)
filed, commented, quiet, uncertain, dismissed = [], [], [], [], []


def band(v, q="actionable"):
    return v["answers"][q]["band"], v["answers"][q].get("noul", 0.0)


def body_of(f, v, previous=None):
    a = v["answers"]
    lines = [
        f"**Severity:** {f['severity']} · **Component:** {f['component']} · "
        f"**Urgency:** {a['urgency']['choice']} · **Actionable:** {a['actionable']['noul']:.2f} ({a['actionable']['band']})",
    ]
    if previous:
        lines += ["", f"> Regression: this was reported before in #{previous['number']} (closed) and is happening again."]
    lines += ["", "## What is wrong", f["summary"], "", "## Impact", f["impact"],
              "", "## Evidence", *[f"- `{e}`" if len(e) < 200 and "`" not in e else f"- {e}" for e in f["evidence"]],
              "", "## Probable cause", f["probable_cause"]]
    if f.get("code_refs"):
        lines += ["", "## Where in the code", *[f"- `{r}`" for r in f["code_refs"]]]
    lines += ["", "## Suggested fix", f["suggested_fix"], "", f"**Who applies it:** {f.get('owner', 'unknown')}",
              "", "---",
              f"*Filed by the `{WORKFLOW}` wfnexus workflow from read-only production signals for {PRODUCT}. "
              "Verify before acting; the classifier's number is a judgement, not a proof.*",
              *[f"<!-- {MARK} {x} -->" for x in [f["fingerprint"], *f.get("covers", [])]]]
    return "\n".join(lines)


def comment_on(issue, f, why):
    last = max([c["createdAt"] for c in issue.get("comments") or []] + [issue["createdAt"]])
    age_h = (now - dt.datetime.fromisoformat(last.replace("Z", "+00:00"))).total_seconds() / 3600
    known = {x for t in [issue.get("body") or ""] + [c.get("body") or "" for c in issue.get("comments") or []]
             for x in MARK_RX.findall(t)}
    new_marks = [x for x in [f["fingerprint"], *f.get("covers", [])] if x not in known]
    # A mapping that is new is worth saying at once; "still happening" waits.
    if age_h < EVERY_H and not new_marks:
        quiet.append(issue["number"])
        return
    note = (f"{why} ({now:%Y-%m-%d %H:%M} UTC).\n\n" + "\n".join(f"- `{e}`" for e in f["evidence"][:6])
            + "".join(f"\n<!-- {MARK} {x} -->" for x in new_marks))
    if not DRY:
        gh("issue", "comment", str(issue["number"]), "-R", REPO, "--body", note)
    commented.append(f"{'(dry run) ' if DRY else ''}#{issue['number']}: {why}")


# 1. Every candidate must be explained by a finding.
explained = set()
for f in triage["findings"]:
    explained.add(f["fingerprint"])
    explained.update(f.get("covers", []))
for c in signals["candidates"]:
    if c["fingerprint"] not in explained:
        uncertain.append({"fingerprint": c["fingerprint"], "title": c["title"], "evidence": c["evidence"][:3],
                          "why": "the triage step returned no finding for this candidate"})

# 2. File, merge, comment or dismiss each finding.
for f in triage["findings"]:
    v = verdicts.get(f["fingerprint"])
    if not v or v.get("error"):
        uncertain.append({**f, "why": "the classifier could not judge it: " + ((v or {}).get("error") or "no verdict")})
        continue
    b, p = band(v)
    if not f.get("in_scope", True):
        dismissed.append((f["fingerprint"], f["title"], "not ours: " + f.get("scope_note", "")))
        continue
    if b == "no":
        dismissed.append((f["fingerprint"], f["title"], round(p, 2)))
        continue
    if b == "uncertain":
        uncertain.append({**f, "why": f"actionable={p:.2f}"})
        continue
    issue = open_by_fp.get(f["fingerprint"]) or next((open_by_fp[x] for x in f.get("covers", []) if x in open_by_fp), None)
    if issue:
        comment_on(issue, f, "Still happening")
        continue
    target = f.get("existing_issue") or 0
    if target and target in open_by_no:
        d = dups.get(f["fingerprint"])
        db, dp = band(d, "same_problem") if d and not d.get("error") else ("uncertain", 0.0)
        if db == "yes":
            comment_on(open_by_no[target], f, f"Same problem, seen by {WORKFLOW}")
            continue
        if db == "uncertain":
            uncertain.append({**f, "why": f"may duplicate #{target} (same_problem={dp:.2f})"})
            continue
    previous = closed_by_fp.get(f["fingerprint"])
    title = f"[{LABEL}] {f['title']}"[:180]
    if DRY:
        filed.append(f"(dry run) {title}" + (f" — regression of #{previous['number']}" if previous else ""))
    else:
        url = gh("issue", "create", "-R", REPO, "--title", title, "--label", LABEL,
                 "--body-file", "-", input=body_of(f, v, previous)).strip()
        filed.append(url)

summary = {"dry_run": DRY, "filed": filed, "commented": commented, "already_reported_recently": quiet,
           "dismissed": dismissed, "uncertain": [f"{u['title']} — {u['why']}" for u in uncertain]}
print(json.dumps(summary, indent=1, default=str))
json.dump(uncertain, open("uncertain.json", "w"), indent=1, default=str)
if uncertain and DRY:
    print(f"(dry run) would stop and ask a person about {len(uncertain)} finding(s)")
sys.exit(3 if uncertain and not DRY else 0)
