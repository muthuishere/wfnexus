#!/usr/bin/env python3
"""act.py — carry out the actions a person just approved (actions.json), and
nothing else. Only two kinds exist: close (with a reason and a comment) and
label. Each is re-checked against the live issue first: an issue a person
already closed or relabelled is left alone.

Env: ISSUE_REPO.
"""
import json
import os
import subprocess
import sys

REPO = os.environ["ISSUE_REPO"]


def gh(*args):
    r = subprocess.run(["gh", *args], capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(r.stderr.strip()[:300])
    return r.stdout


done, skipped, failed = [], [], []
for a in json.load(open("actions.json")):
    n = str(a["issue"])
    try:
        now = json.loads(gh("issue", "view", n, "-R", REPO, "--json", "state,labels"))
        if now["state"] != "OPEN":
            skipped.append(f"#{n}: already {now['state'].lower()}")
            continue
        if a.get("label"):
            gh("issue", "edit", n, "-R", REPO, "--add-label", a["label"])
        if a["kind"] == "close":
            gh("issue", "close", n, "-R", REPO, "--reason", a["reason"], "--comment", a["comment"])
        done.append(a["what"])
    except Exception as e:
        failed.append(f"{a['what']}: {e}")
print(json.dumps({"done": done, "skipped": skipped, "failed": failed}, indent=1))
sys.exit(1 if failed else 0)
