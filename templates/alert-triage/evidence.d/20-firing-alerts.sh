#!/usr/bin/env bash
# Alerts Prometheus is firing right now (GET /api/v1/alerts), over OBS_SSH when set.
set -uo pipefail
PROM_URL="${PROM_URL:-}"
[ -n "$PROM_URL" ] || { echo "PROM_URL not set — firing alerts unknown"; exit 0; }
if [ -n "${OBS_SSH:-}" ]; then
  out=$(ssh -o BatchMode=yes -o ConnectTimeout=10 -o ControlMaster=auto -o ControlPath=/tmp/wfx-cm-%C -o ControlPersist=60 \
        "$OBS_SSH" "curl -sS -m 20 '$PROM_URL/api/v1/alerts'") || { echo "could not reach Prometheus"; exit 0; }
else
  out=$(curl -sS -m 20 "$PROM_URL/api/v1/alerts") || { echo "could not reach Prometheus"; exit 0; }
fi
printf '%s' "$out" | python3 -c "
import json, sys
d = json.load(sys.stdin)
a = d.get('data', {}).get('alerts', [])
print(f'{len(a)} alert(s) active')
for x in a:
    l = x.get('labels', {})
    print('-', x.get('state'), l.get('alertname'), {k: v for k, v in l.items() if k != 'alertname'}, 'since', x.get('activeAt'))
"
