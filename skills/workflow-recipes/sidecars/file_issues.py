#!/usr/bin/env python3
"""file_issues.py — file one GitHub issue per finding, never twice.

  python3 file_issues.py [yes.jsonl] [--only id1,id2]

Each line: {"id": <fingerprint>, "title": ..., "body": ...} (route.py output).
- One issue per FINGERPRINT, found again by a marker in its body.
- An open issue with the same fingerprint gets a comment at most every
  COMMENT_EVERY_H hours (default 24) — a 15-minute watch must not post 96
  comments a day about the same problem.
- --only files just the named ids (the escalate step uses it for the ones a
  person picked out of uncertain.jsonl).

Env: REPO (owner/name, required), LABEL (required), DRY_RUN=true prints only,
COMMENT_EVERY_H. Needs `gh` authenticated on the machine that runs the step.
"""
import datetime as dt
import json
import os
import subprocess
import sys

REPO, LABEL = os.environ["REPO"], os.environ["LABEL"]
DRY = os.environ.get("DRY_RUN", "") in ("1", "true", "yes")
EVERY_H = float(os.environ.get("COMMENT_EVERY_H", "24"))
MARK = "wfx-fingerprint:"

args = sys.argv[1:]
only = None
if "--only" in args:
    i = args.index("--only")
    only = set(args[i + 1].split(","))
    del args[i:i + 2]
path = args[0] if args else "yes.jsonl"


def gh(*a):
    r = subprocess.run(["gh", *a], capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"gh {' '.join(a[:2])} failed: {r.stderr.strip()[:300]}")
    return r.stdout


rows = [json.loads(line) for line in open(path) if line.strip()]
if only is not None:
    rows = [r for r in rows if r["id"] in only]

existing = {}
for i in json.loads(gh("issue", "list", "-R", REPO, "--label", LABEL, "--state", "open",
                       "--limit", "200", "--json", "number,body,comments,createdAt,url")):
    for line in (i.get("body") or "").splitlines():
        if MARK in line:
            existing[line.split(MARK, 1)[1].strip(" ->")] = i

now = dt.datetime.now(dt.timezone.utc)
out = {"filed": [], "commented": [], "already_reported_recently": []}
for r in rows:
    body = (r.get("body") or r.get("state") or "") + f"\n\n<!-- {MARK} {r['id']} -->\n"
    hit = existing.get(r["id"])
    if hit:
        stamps = [c.get("createdAt") for c in hit.get("comments") or []] + [hit["createdAt"]]
        last = max(dt.datetime.fromisoformat(s.replace("Z", "+00:00")) for s in stamps if s)
        if (now - last).total_seconds() < EVERY_H * 3600:
            out["already_reported_recently"].append(hit["url"])
            continue
        if not DRY:
            gh("issue", "comment", str(hit["number"]), "-R", REPO, "--body", "Still happening.\n\n" + body)
        out["commented"].append(("(dry run) " if DRY else "") + hit["url"])
        continue
    title = r.get("title") or r["id"]
    if DRY:
        out["filed"].append(f"(dry run) {title}")
        continue
    url = gh("issue", "create", "-R", REPO, "--label", LABEL, "--title", title, "--body", body).strip()
    out["filed"].append(url)

print(json.dumps(out, indent=1))
