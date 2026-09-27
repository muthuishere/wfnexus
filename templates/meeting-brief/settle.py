#!/usr/bin/env python3
"""settle.py — remember which meetings were briefed (workflow state `briefed`,
pruned to 14 days) so a 15-minute sweep never briefs one twice. Dry runs
touch nothing. Exit 10 = nothing to deliver (skip the approval gate)."""
import datetime as dt, json, os, subprocess, sys
dry = os.environ.get("DRY_RUN") == "true"
deliver = os.environ.get("DELIVER", "none")
ev = json.load(open("evidence.json"))
if dry:
    print(f"dry run: {len(ev.get('meetings', []))} meeting(s) briefed; state untouched, nothing delivered")
    sys.exit(10)
p = subprocess.run(["wfx", "state", "get", "--workflow", "briefed"], capture_output=True, text=True)
try:
    seen = json.loads(p.stdout.strip() or "{}")
except json.JSONDecodeError:
    seen = {}
now = dt.datetime.now(dt.timezone.utc)
cut = (now - dt.timedelta(days=14)).isoformat()
seen = {k: v for k, v in seen.items() if v >= cut}
for r in ev.get("meetings", []):
    seen[r] = now.isoformat()
subprocess.run(["wfx", "state", "set", "--workflow", "briefed", json.dumps(seen)], check=True)
print(f"recorded {len(ev.get('meetings', []))} briefed meeting(s); {len(seen)} remembered")
sys.exit(10 if deliver == "none" else 0)
