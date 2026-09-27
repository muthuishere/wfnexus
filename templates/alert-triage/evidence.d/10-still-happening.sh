#!/usr/bin/env bash
# Is the signal behind this alert still there NOW? Re-runs the same rule
# files prod-watch uses (collect.py + CHECKS_DIR), once per run, and says for
# each fingerprint on the issue whether a rule still raises it. A fingerprint
# no rule raises any more is evidence the problem stopped — or that the check
# changed; the triage must say which.
set -uo pipefail
cd "$WFX_WORKSPACE" 2>/dev/null || cd "$(dirname "$0")/.."
if [ ! -f ./collect.py ] || { [ -z "${CHECKS_DIR:-}" ] && [ -z "${LOKI_QUERY:-}" ]; }; then
  echo "no live checks configured (CHECKS_DIR / LOKI_QUERY) — 'still happening' is unknown"; exit 0
fi
if [ ! -s signals_now.json ]; then
  WINDOW_MIN="${WINDOW_MIN:-60}" python3 ./collect.py > signals_now.json 2> signals_now.err
  rc=$?; [ $rc -eq 0 ] || [ $rc -eq 3 ] || { echo "live checks failed (exit $rc):"; tail -3 signals_now.err; exit 0; }
fi
python3 - "$ISSUE_FILE" <<'PY'
import json, sys
issue = json.load(open(sys.argv[1]))
now = json.load(open("signals_now.json"))
live = {c["fingerprint"]: c for c in now["candidates"]}
print("checked at:", now.get("window_min"), "minute window; sources:", json.dumps(now["sources"]))
fps = issue.get("fingerprints") or []
if not fps:
    print("the issue carries no wfx-fingerprint marker (filed by hand or another tool) — match by content below")
for f in fps:
    c = live.get(f)
    print(f"fingerprint {f}: " + ("STILL RAISED NOW — " + c["title"] + " | " + " ; ".join(c["evidence"][:3]) if c else "NOT raised now"))
print("\nevery candidate the rules raise right now:")
for c in now["candidates"]:
    print(f"- {c['fingerprint']} {c['severity']} {c['title']} | {' ; '.join(c['evidence'][:2])[:400]}")
PY
