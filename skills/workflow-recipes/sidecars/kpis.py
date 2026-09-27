#!/usr/bin/env python3
"""kpis.py — pull each KPI, compare to last week and to a second source.

  python3 kpis.py kpis.yaml <material_pct>

kpis.yaml:
  - name: mrr
    this_week: psql "$DB_URL" -Atc "select ..."      # prints ONE number
    last_week: psql "$DB_URL" -Atc "select ..."
    reconcile: stripe-mrr.sh                           # optional: same metric, other source

Deterministic — no model. Any command that fails or prints no number exits 1:
a report on partial numbers is worse than no report.
Writes kpis.json (everything) and moves.jsonl (only moves/gaps past the threshold,
in the {"id","state"} shape `wfx judge` reads).
"""
import json
import re
import subprocess
import sys

import yaml

NUM = re.compile(r"-?\d+(?:\.\d+)?")
kpis = yaml.safe_load(open(sys.argv[1])) or []
pct = float(sys.argv[2]) if len(sys.argv) > 2 else 10.0


def number(cmd, what):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=120)
    m = NUM.search(r.stdout or "")
    if r.returncode != 0 or not m:
        sys.exit(f"{what}: exit {r.returncode}, no number; stderr={r.stderr.strip()[:300]!r}")
    return float(m.group(0))


rows, moves = [], []
for k in kpis:
    now = number(k["this_week"], k["name"] + ".this_week")
    prev = number(k["last_week"], k["name"] + ".last_week")
    delta = (now - prev) / prev * 100 if prev else (0.0 if now == 0 else 100.0)
    row = {"name": k["name"], "this_week": now, "last_week": prev, "delta_pct": round(delta, 1),
           "query": k["this_week"]}
    if k.get("reconcile"):
        other = number(k["reconcile"], k["name"] + ".reconcile")
        row["reconcile"] = other
        row["gap_pct"] = round(abs(now - other) / other * 100, 1) if other else (0.0 if now == 0 else 100.0)
    rows.append(row)
    if abs(delta) >= pct or row.get("gap_pct", 0) > 2:
        moves.append({"id": k["name"], "state":
                      f"KPI {k['name']}: this week {now:g}, last week {prev:g}, change {delta:+.1f}% "
                      f"(threshold {pct:g}%)." + (f" Second source says {row['reconcile']:g} — a {row['gap_pct']}% gap."
                                                   if "reconcile" in row else "")})

json.dump(rows, open("kpis.json", "w"), indent=1)
with open("moves.jsonl", "w") as f:
    for m in moves:
        f.write(json.dumps(m) + "\n")
print(json.dumps(rows))
