#!/usr/bin/env python3
"""collect.py — the day's raw material, read-only, from every connected account.

This is apl-skill's BRIEF-1 "Morning Brief (full)" (references/morning-brief.md)
run as a deterministic collector, plus what a leader's morning needs that
BRIEF-1 does not have: sent mail (to find my promises), overdue tasks,
documents shared with me, open sre-alert issues, and the action items that
meeting-actions left in project state.

Writes evidence.json (what the brief may cite) and evidence.md (the compact
digest the drafting agent reads). Prints one line per source. Exit 3 when no
source at all could be read — a brief built from nothing is not a brief.

Env (set from the workflow's inputs): DAY, TZ_NAME, LOOKBACK_H, ACCOUNTS,
GH_IDENTITIES, GH_SCOPE, ALERT_LABEL, INTERNAL_DOMAINS.
"""
import datetime as dt
import json
import os
import sys
from zoneinfo import ZoneInfo

import sources as S

tz = ZoneInfo(os.environ["TZ_NAME"]) if os.environ.get("TZ_NAME") else dt.datetime.now().astimezone().tzinfo
day = os.environ.get("DAY", "").strip()
d0 = dt.datetime.fromisoformat(day).replace(tzinfo=tz) if day else \
    dt.datetime.now(tz).replace(hour=0, minute=0, second=0, microsecond=0)
d1 = d0 + dt.timedelta(days=1)
lookback = float(os.environ.get("LOOKBACK_H") or 0) or (72 if d0.weekday() == 0 else 24)
since = min(S.now_utc(), d0.astimezone(dt.timezone.utc) + dt.timedelta(hours=9)) - dt.timedelta(hours=lookback)
sent_since = since - dt.timedelta(hours=24)  # promises made the day before too

accts = S.accounts(os.environ.get("ACCOUNTS", ""))
E = S.Evidence("daily-brief", {"from": S.iso(since), "to": S.iso(d1), "day": d0.date().isoformat(),
                               "tz": str(tz), "lookback_h": lookback})
internal = S.internal_domains(accts, os.environ.get("INTERNAL_DOMAINS", ""))
me = {a["email"] for a in accts}
E.doc["me"] = {"accounts": [a["handle"] for a in accts], "emails": sorted(me), "internal_domains": sorted(internal)}
if not accts:
    E.source("apl accounts", "unreadable", reason="apl has no google:/ms: account",
             fix="apl setup google && apl login google:<label>  (or apl setup ms)")

# MAIL-X-3 cross-check — the skill's own sweep, across every handle.
S.mail_sweep(E, [a["handle"] for a in accts])


def per_account(a):
    cal = S.calendar(E, a, d0.astimezone(dt.timezone.utc), d1.astimezone(dt.timezone.utc))
    inbox = S.mail(E, a, since, "inbox", cap=40)
    sent = S.mail(E, a, sent_since, "sent", cap=25)
    S.mark_replied(E, inbox, sent)
    S.chats(E, a, since)
    S.tasks(E, a, d1.astimezone(dt.timezone.utc))
    S.drive_shared(E, a, since)
    return a, cal


results = S.pmap(per_account, accts, 4)

# What each meeting needs: who is in it, its docs, the last thread with the
# first outside attendee, files named like it.
by_handle = {a["handle"]: a for a in accts}
for a, cal in results:
    for ref in cal[:10]:
        ev = E.doc["items"][ref]
        if ev.get("all_day"):
            continue
        cls = S.meeting_class(ev, internal, me)
        ev.update(cls)
        ext = [x["email"] for x in ev.get("attendees", []) if x.get("email") and x["email"] not in me
               and not S.is_internal(x["email"], internal)]
        if ext:
            S.person(E, accts, ext[0])
            ev["last_thread_with"] = ext[0]
            ev["related"] = S.mail_with(E, a, ext[0], days=90, cap=3)
        ev["related_files"] = S.drive_search(E, a, ev.get("title", ""), cap=3,
                                             name=f"drive DRIVE-14 for '{ev.get('title','')[:30]}' {a['handle']}")

# GitHub — BRIEF-3 queue (GH-5/6/7/16/17) + monitoring, for EVERY bound identity.
alert = os.environ.get("ALERT_LABEL") or "sre-alert"
for ident in S.gh_identities(os.environ.get("GH_IDENTITIES", "")):
    login, err = S.github_login(ident)
    if err:
        E.source(f"github {ident or 'gh'}", "unreadable", reason=err,
                 fix=f"apl login {ident}" if ident else "gh auth login")
        continue
    E.doc["me"].setdefault("github", []).append(login)
    S.gh_search(E, "is:open review-requested:@me", "GH-6 review requested", ident=ident)
    S.gh_search(E, "is:open assignee:@me", "GH-5 assigned PRs", ident=ident)
    S.gh_search(E, "is:open author:@me", "GH-7 my open PRs", cap=20, ident=ident)
    S.gh_search(E, "is:open assignee:@me", "GH-16 assigned issues", kind="issues", ident=ident)
    S.gh_search(E, f"mentions:@me updated:>={since.date().isoformat()}", "GH-17 mentions", kind="issues",
                cap=15, ident=ident)
    scope = S.github_scope(login, os.environ.get("GH_SCOPE", ""), ident)
    S.gh_search(E, f"is:open label:{alert} {scope}", f"monitoring {alert} open", kind="issues", cap=20, ident=ident)

# Commitments left by meeting-actions (project state) — cited to their meeting.
raw = S.wfx_state("project", "lead_open_actions")
try:
    acts = json.loads(raw) if raw else []
    for x in acts:
        if x.get("done"):
            continue
        E.add(f"action:{x['id']}", "action", title=x.get("text", "")[:200], owner=x.get("owner"),
              due=x.get("due"), meeting=x.get("meeting"), meeting_title=x.get("meeting_title"),
              quote=x.get("quote", "")[:300], recorded=x.get("recorded"))
    E.source("open actions (meeting-actions state)", "ok", len([x for x in acts if not x.get("done")]))
except (json.JSONDecodeError, KeyError, TypeError):
    E.source("open actions (meeting-actions state)", "unreadable", reason="state value is not JSON")

E.dump("evidence.json")
E.digest("evidence.md")
ok = [s for s in E.doc["sources"] if s["status"] == "ok"]
for s in E.doc["sources"]:
    print(f"{s['status']:10} {s['count']:4}  {s['name']}" + (f"  → {s.get('fix','')}" if s['status'] != 'ok' else ""))
print(f"items: {len(E.doc['items'])}  sources ok: {len(ok)}/{len(E.doc['sources'])}")
sys.exit(0 if ok else 3)
