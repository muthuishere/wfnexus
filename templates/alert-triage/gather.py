#!/usr/bin/env python3
"""gather.py — pick the alert issues to triage and collect their evidence.

Deterministic: which issues, which related issues, what changed, and whether
the signal is still there are all decided here, before any model runs.

  ISSUE=<n>          triage exactly that issue (repository_dispatch / a person)
  ISSUE=0            sweep: open ALERT_LABEL issues with no current triage —
                     no `wfx-triage` marker, or a human comment newer than it
  MAX_ISSUES         cap per sweep (default 5)

Per issue it writes evidence/<n>/:
  issue.json         title, body, labels, comments (as filed)
  related.json       shortlist of possible duplicates: same fingerprint marker,
                     or title overlap — open AND closed (a recurrence is a lead)
  <script>.txt       the output of each executable in evidence.d/ (live
                     signals, recent changes, firing alerts …), given
                     ISSUE_NUMBER, ISSUE_TITLE, ISSUE_FILE, EVIDENCE_DIR

Writes targets.json. Exit 3 = something to triage, 0 = nothing, 2 = broken.
Read-only: gh issue list/view only.
"""
import json
import os
import re
import subprocess
import sys

REPO = os.environ["ISSUE_REPO"]
LABEL = os.environ.get("ALERT_LABEL", "sre-alert")
ISSUE = int(os.environ.get("ISSUE") or 0)
MAX = int(os.environ.get("MAX_ISSUES") or 5)
TRIAGE_MARK = "wfx-triage:"
FP_RX = re.compile(r"wfx-fingerprint:\s*([0-9a-f]{6,40})")
STOP = set("the a an of in on for to and is are be not no with from by at as it its this that "
           "sre alert ops error failed failing".split())


def gh_json(*args):
    r = subprocess.run(["gh", *args], capture_output=True, text=True)
    if r.returncode != 0:
        print(f"gh {' '.join(args[:3])} failed: {r.stderr.strip()[:300]}", file=sys.stderr)
        sys.exit(2)
    return json.loads(r.stdout)


def words(t):
    t = re.sub(r"\[[^\]]*\]", " ", t.lower())
    return {w for w in re.findall(r"[a-z0-9_]{3,}", t) if w not in STOP}


FIELDS = "number,title,body,labels,comments,createdAt,updatedAt,state,url"
everything = gh_json("issue", "list", "-R", REPO, "--label", LABEL, "--state", "all", "--limit", "300",
                     "--json", "number,title,body,labels,state,createdAt,closedAt")


def needs_triage(i):
    comments = i.get("comments") or []
    marks = [c for c in comments if TRIAGE_MARK in (c.get("body") or "")]
    if not marks:
        return True
    last_mark = max(c["createdAt"] for c in marks)
    # someone said something new since the last triage: look again
    return any(c["createdAt"] > last_mark for c in comments if TRIAGE_MARK not in (c.get("body") or ""))


if ISSUE:
    targets = [gh_json("issue", "view", str(ISSUE), "-R", REPO, "--json", FIELDS)]
else:
    open_ones = gh_json("issue", "list", "-R", REPO, "--label", LABEL, "--state", "open", "--limit", "100",
                        "--json", FIELDS)
    targets = [i for i in sorted(open_ones, key=lambda i: i["createdAt"]) if needs_triage(i)][:MAX]

os.makedirs("evidence", exist_ok=True)
scripts = sorted(p for p in (os.path.join("evidence.d", f) for f in (os.listdir("evidence.d") if os.path.isdir("evidence.d") else []))
                 if os.path.isfile(p) and os.access(p, os.X_OK))
summary = []
for t in targets:
    n = t["number"]
    d = os.path.join("evidence", str(n))
    os.makedirs(d, exist_ok=True)
    t["labels"] = [l["name"] if isinstance(l, dict) else l for l in t.get("labels") or []]
    t["fingerprints"] = sorted(set(FP_RX.findall((t.get("body") or "") + "".join(c.get("body") or "" for c in t.get("comments") or []))))
    t["comments"] = [{"author": (c.get("author") or {}).get("login"), "createdAt": c["createdAt"], "body": (c.get("body") or "")[:3000]}
                     for c in t.get("comments") or []]
    json.dump(t, open(os.path.join(d, "issue.json"), "w"), indent=1)

    # Shortlist of possible duplicates, scored deterministically: a shared
    # fingerprint first, then title overlap, then body overlap (a symptom's
    # body names its cause: "jobs not cleaned up" mentions the failing cron).
    mine, mine_body = words(t["title"]), words(t["title"] + " " + (t.get("body") or "")[:3000])
    scored = []
    for o in everything:
        if o["number"] == n:
            continue
        ofps = set(FP_RX.findall(o.get("body") or ""))
        ot, ob = words(o["title"]), words(o["title"] + " " + (o.get("body") or "")[:3000])
        title_ov = len(mine & ot) / max(1, len(mine | ot))
        body_ov = len(mine_body & ob) / max(1, len(mine_body | ob))
        same = bool(ofps & set(t["fingerprints"]))
        scored.append(({"number": o["number"], "title": o["title"], "state": o["state"],
                        "createdAt": o["createdAt"], "closedAt": o.get("closedAt"), "same_fingerprint": same,
                        "title_overlap": round(title_ov, 2), "body_overlap": round(body_ov, 2),
                        "body_head": (o.get("body") or "")[:800]}, (same, title_ov >= 0.3, body_ov)))
    scored.sort(key=lambda x: x[1], reverse=True)
    related = [r for r, (same, t_hit, b) in scored if same or t_hit or b >= 0.12][:6]
    for r, _ in scored:  # always show the closest OPEN alerts, so a cause filed separately is on the table
        if len(related) >= 6:
            break
        if r["state"] == "OPEN" and r not in related:
            related.append(r)
    json.dump(related, open(os.path.join(d, "related.json"), "w"), indent=1)

    env = {**os.environ, "ISSUE_NUMBER": str(n), "ISSUE_TITLE": t["title"],
           "ISSUE_FILE": os.path.abspath(os.path.join(d, "issue.json")), "EVIDENCE_DIR": os.path.abspath(d),
           "ISSUE_CREATED": t["createdAt"]}
    ran = []
    for s in scripts:
        name = os.path.basename(s)
        try:
            r = subprocess.run([os.path.abspath(s)], env=env, capture_output=True, text=True, timeout=240)
            out = (r.stdout or "")[:20000] + (f"\n[stderr]\n{r.stderr[-2000:]}" if r.returncode else "")
            ran.append(f"{name}:{'ok' if r.returncode == 0 else 'exit ' + str(r.returncode)}")
        except subprocess.TimeoutExpired:
            out = "[timed out after 240s — this evidence is missing, say so]"
            ran.append(f"{name}:timeout")
        open(os.path.join(d, os.path.splitext(name)[0] + ".txt"), "w").write(out)
    summary.append({"number": n, "title": t["title"], "fingerprints": t["fingerprints"],
                    "related": [r["number"] for r in related[:6]], "evidence": ran})

json.dump(summary, open("targets.json", "w"), indent=1)
for s in summary:
    print(f"#{s['number']} {s['title'][:90]} | related {s['related']} | {', '.join(s['evidence'])}")
if not summary:
    print("nothing to triage")
sys.exit(3 if summary else 0)
