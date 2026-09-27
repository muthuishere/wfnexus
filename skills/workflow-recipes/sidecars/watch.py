#!/usr/bin/env python3
"""watch.py — run the signals in signals.yaml and say which ones are wrong.

Deterministic on purpose: a CANDIDATE comes from a rule over a number, never
from a model. The agent after this explains a candidate; it may not invent one.

signals.yaml:
  - name: failed_payments_15m          # stable id — part of the fingerprint
    title: "Failed payments in the last 15 minutes"
    command: psql "$PAYMENTS_DB_URL" -Atc "select count(*) from payments where status='failed' and created_at > now() - interval '15 minutes'"
    op: ">"                             # > >= < <= == !=
    threshold: 5
    severity: error                     # critical | error | warning

The command must print ONE number (the first number in stdout is used).
A command that fails, times out or prints no number is a candidate of its own
(kind source_broken): silence from a broken check never reads as healthy.

Writes signals.json. Exit 0 = healthy, 3 = candidates (the step's ANSWER, not
a failure — the next steps are guarded on it).
"""
import hashlib
import json
import operator
import re
import subprocess
import sys

import yaml

OPS = {">": operator.gt, ">=": operator.ge, "<": operator.lt, "<=": operator.le,
       "==": operator.eq, "!=": operator.ne}
NUM = re.compile(r"-?\d+(?:\.\d+)?")


def fp(*parts):
    return hashlib.sha1("|".join(parts).encode()).hexdigest()[:12]


signals = yaml.safe_load(open(sys.argv[1] if len(sys.argv) > 1 else "signals.yaml")) or []
if not signals:
    sys.exit("signals.yaml lists no signals — a watch with nothing to watch is not a watch")

sources, candidates = {}, []
for s in signals:
    name = s["name"]
    try:
        r = subprocess.run(s["command"], shell=True, capture_output=True, text=True,
                           timeout=int(s.get("timeout_sec", 60)))
        m = NUM.search(r.stdout or "")
        if r.returncode != 0 or not m:
            raise RuntimeError(f"exit {r.returncode}; stdout={r.stdout.strip()[:200]!r}; "
                               f"stderr={r.stderr.strip()[:300]!r}")
        value = float(m.group(0))
    except Exception as e:  # noqa: BLE001 — any failure is reported, never swallowed
        sources[name] = f"broken: {e}"
        candidates.append({"fingerprint": fp("source_broken", name), "kind": "source_broken",
                           "signal": name, "severity": "error",
                           "title": f"Check '{name}' could not be read",
                           "evidence": [str(e)[:600]]})
        continue
    bad = OPS[s.get("op", ">")](value, float(s["threshold"]))
    sources[name] = f"ok: {value:g} ({'BREACH' if bad else 'fine'} vs {s.get('op', '>')} {s['threshold']})"
    if bad:
        candidates.append({"fingerprint": fp("breach", name), "kind": "breach", "signal": name,
                           "severity": s.get("severity", "error"), "title": s.get("title", name),
                           "value": value, "threshold": s["threshold"], "op": s.get("op", ">"),
                           "evidence": [f"{name} = {value:g}, rule {s.get('op', '>')} {s['threshold']}",
                                        f"command: {s['command'][:300]}"]})

out = {"healthy": not candidates, "sources": sources, "candidates": candidates}
json.dump(out, open("signals.json", "w"), indent=1)
print("sources:", json.dumps(sources))
for c in candidates:
    print("-", c["severity"], c["kind"], c["fingerprint"], c["title"])
sys.exit(0 if not candidates else 3)
