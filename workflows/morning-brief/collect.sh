#!/bin/bash
# Gathers the day's raw material into one JSON document on stdout.
#
# Every source is optional BY DESIGN. A laptop where gcloud's token expired
# still deserves a brief about the other five things, so each source records
# either its rows or why it has none — and the agent downstream is told the
# difference. A collector that dies on the first expired token is a collector
# that silently stops reporting.
set -uo pipefail
GMAIL="https://gmail.googleapis.com/gmail/v1/users/me"
CAL="https://www.googleapis.com/calendar/v3/calendars/primary/events"

jqsafe() { python3 -c "import json,sys;print(json.dumps(sys.stdin.read()))"; }
note()   { python3 -c "import json,sys;print(json.dumps({'unavailable':sys.argv[1]}))" "$1"; }

# ── mail: the primary inbox, last 3 days, per account ───────────────────────
mail_for() {
  local h="$1"
  local ids
  ids=$(apl call "$h" GET "$GMAIL/messages?q=in:inbox%20newer_than:3d%20category:primary&maxResults=25" 2>/dev/null \
        | python3 -c "import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
print(' '.join(m['id'] for m in d.get('messages',[])))" ) || { note "$h: not logged in (apl login $h)"; return; }
  for id in $ids; do
    apl call "$h" GET "$GMAIL/messages/$id?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date" 2>/dev/null \
    | python3 -c "
import json,sys
try: m=json.load(sys.stdin)
except Exception: sys.exit()
h={x['name']:x['value'] for x in m.get('payload',{}).get('headers',[])}
print(json.dumps({'account':'$h','from':h.get('From','')[:70],'subject':h.get('Subject','')[:110],'date':h.get('Date','')[:25],'snippet':(m.get('snippet') or '')[:300]}))"
  done
}

# ── calendar: what is actually on today ─────────────────────────────────────
cal_for() {
  local h="$1" from to
  from=$(date -u +%Y-%m-%dT00:00:00Z); to=$(date -u -v+1d +%Y-%m-%dT00:00:00Z 2>/dev/null || date -u -d '+1 day' +%Y-%m-%dT00:00:00Z)
  apl call "$h" GET "$CAL?timeMin=$from&timeMax=$to&singleEvents=true&orderBy=startTime&maxResults=20" 2>/dev/null \
  | python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit()
for e in d.get('items',[]):
    s=e.get('start',{})
    print(json.dumps({'account':'$h','when':s.get('dateTime') or s.get('date',''),'what':(e.get('summary') or '')[:90]}))" 2>/dev/null
}

# ── github: what is waiting on ME, not everything open ──────────────────────
gh_q() {
  gh api "/search/issues?q=$1&per_page=15" --jq '.items[] | {repo: (.repository_url|split("/")|.[-2:]|join("/")), number, title, updated: .updated_at, url: .html_url}' -q . 2>/dev/null \
  | python3 -c "
import json,sys
for l in sys.stdin:
    l=l.strip()
    if not l: continue
    try: d=json.loads(l)
    except Exception: continue
    d['kind']='$2'; print(json.dumps(d))"
}

# ── gcloud: errors worth waking up to ───────────────────────────────────────
gcloud_errors() {
  local proj; proj=$(gcloud config list --format='value(core.project)' 2>/dev/null)
  [ -z "$proj" ] && { note "no gcloud project configured"; return; }
  local out
  out=$(gcloud logging read 'severity>=ERROR' --project "$proj" --limit 15 --freshness=1d \
        --format='value(timestamp,resource.type,textPayload)' 2>&1)
  if printf '%s' "$out" | grep -qi "reauthentication\|auth login\|credentials"; then
    note "gcloud token expired (gcloud auth login)"; return
  fi
  printf '%s\n' "$out" | while IFS= read -r line; do
    [ -z "$line" ] && continue
    python3 -c "import json,sys;print(json.dumps({'project':sys.argv[1],'line':sys.argv[2][:300]}))" "$proj" "$line"
  done
}

collect() { local out; out=$("$@" 2>/dev/null); [ -z "$out" ] && echo "" || printf '%s\n' "$out"; }

python3 - <<PY
import json,subprocess,sys
def lines(cmd):
    p=subprocess.run(['bash','-c',cmd],capture_output=True,text=True)
    out=[]
    for l in p.stdout.splitlines():
        l=l.strip()
        if not l: continue
        try: out.append(json.loads(l))
        except Exception: pass
    return out
src="$0"
doc={
 'mail':        lines(f'source {src}; mail_for google:deemwar; mail_for google:muthu'),
 'calendar':    lines(f'source {src}; cal_for google:deemwar; cal_for google:muthu'),
 'prs_to_review':lines(f'source {src}; gh_q "is:open+is:pr+review-requested:muthuishere" review'),
 'prs_assigned':lines(f'source {src}; gh_q "is:open+is:pr+assignee:muthuishere" assigned'),
 'issues_assigned':lines(f'source {src}; gh_q "is:open+is:issue+assignee:muthuishere" issue'),
 'gcloud_errors':lines(f'source {src}; gcloud_errors'),
}
print(json.dumps(doc, indent=1))
PY
