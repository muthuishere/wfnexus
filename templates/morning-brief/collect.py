#!/usr/bin/env python3
"""morning-brief: gather the facts the owner's brief may cite. Read-only, no AI.

    collect.py   writes facts.json; always exit 0 — a source that cannot be
                 read is NOTED (a `missing:<source>` fact), never a crash

facts.json:
  facts[]     {id, source, text}: the ONLY things a brief line may cite
                run:<id>          a run that failed in the window
                wait:<id>         a run waiting on a person (approval / question)
                pr:<n>            an open PR on $GH_REPO
                issue:<n>         an open issue on $GH_REPO
                now               ceo-harness NOW.md: mtime + its top section
                missing:<source>  a source that could not be read
  previous    the last morning-brief's kept lines (what changed since yesterday)
  missing[]   the sources that could not be read
"""
import datetime as dt
import json
import os
import subprocess
import urllib.request

SELF = os.environ.get("BRIEF_WORKFLOW", "morning-brief")
SINCE_HOURS = float(os.environ.get("SINCE_HOURS", "24"))
REPO = os.environ.get("GH_REPO", "muthuishere/wfnexus")
NOW_MD = os.path.expanduser(os.environ.get("NOW_MD", "~/muthu/deemwarworkspace/ceo-harness/NOW.md"))


def api(path):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    req = urllib.request.Request(base + "/api" + path)
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.loads(r.read() or b"null")


def when(s):
    try:
        return dt.datetime.fromisoformat(str(s).replace("Z", "+00:00"))
    except ValueError:
        return None


def runs(facts):
    rows = api("/runs") or []
    since = dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=SINCE_HOURS)
    failed = {}  # workflow -> its failed runs, newest first: ONE fact per workflow
    for r in sorted(rows, key=lambda r: r.get("createdAt") or "", reverse=True):
        wf, st, rid = r.get("workflow"), r.get("status"), r.get("id")
        if wf == SELF:
            continue
        if st in ("awaiting_approval", "awaiting_input"):  # waiting: however old
            facts.append({"id": f"wait:{rid}", "source": "wfnexus",
                          "text": f"{wf} waits on you at {r.get('currentStep') or '?'} ({st})"})
        elif st == "failed" and (when(r.get("createdAt")) or since) >= since:
            failed.setdefault(wf, []).append(r)
    for wf, rs in failed.items():
        r = rs[0]
        facts.append({"id": f"run:{r.get('id')}", "source": "wfnexus",
                      "text": f"{wf} failed {len(rs)}x in {SINCE_HOURS:g}h, latest at "
                              f"{r.get('currentStep') or '?'}: {(r.get('error') or '')[:160]}"})
    return rows


def previous(rows):
    mine = sorted((r for r in rows if r.get("workflow") == SELF and r.get("status") == "done"
                   and r.get("id") != os.environ.get("WFX_RUN_ID")),
                  key=lambda r: r.get("createdAt") or "")
    if not mine:
        return []
    for s in (api("/runs/" + mine[-1]["id"]) or {}).get("steps") or []:
        if s.get("stepId") == "check":
            try:
                return json.loads((s.get("output") or {}).get("stdout") or "{}").get("lines") or []
            except ValueError:
                return []
    return []


def gh(facts, kind):
    out = subprocess.run([os.environ.get("GH_BIN", "gh"), kind, "list", "-R", REPO, "--state", "open",
                          "--limit", "20", "--json", "number,title,updatedAt"],
                         capture_output=True, text=True, timeout=60, check=True).stdout
    for x in json.loads(out or "[]"):
        facts.append({"id": f"{kind}:{x['number']}", "source": "github",
                      "text": f"{kind.upper()} #{x['number']} open: {x['title']} (updated {str(x.get('updatedAt', '?'))[:10]})"})


def now_md(facts):
    st = os.stat(NOW_MD)
    top, heads = [], 0
    with open(NOW_MD, encoding="utf-8", errors="replace") as f:
        for line in f:
            if line.startswith("## "):
                heads += 1
                if heads > 1:
                    break
            if line.strip():
                top.append(line.strip())
    mtime = dt.datetime.fromtimestamp(st.st_mtime).strftime("%Y-%m-%d %H:%M")
    facts.append({"id": "now", "source": "ceo-harness",
                  "text": f"NOW.md last edited {mtime}. Top: " + " / ".join(top[:12])[:900]})


def main():
    facts, missing, prev = [], [], []
    try:
        prev = previous(runs(facts))
    except Exception as e:
        missing.append(("wfnexus", repr(e)[:200]))
    for kind in ("pr", "issue"):
        try:
            gh(facts, kind)
        except Exception as e:
            missing.append((f"github-{kind}", repr(e)[:200]))
    try:
        now_md(facts)
    except Exception as e:
        missing.append(("ceo-harness", repr(e)[:200]))
    for src, why in missing:
        facts.append({"id": f"missing:{src}", "source": src, "text": f"could not read {src}: {why}"})
    with open("facts.json", "w") as f:
        json.dump({"facts": facts, "previous": prev, "missing": [m[0] for m in missing]}, f, indent=1)
    print(json.dumps({"facts": len(facts), "missing": [m[0] for m in missing]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
