#!/usr/bin/env python3
"""collect.py — pick the meetings about to start that deserve a brief, and read
everything relevant to each, read-only, through apl-skill recipes.

Picks events that start between FROM_MIN and TO_MIN minutes from now (default
25–40, so a 15-minute sweep sees every meeting exactly once), on every
connected calendar, that:
  - are not all-day, not declined, and have someone other than me in them;
  - are external (an attendee outside my domains) — or match KEYWORDS, or
    INCLUDE_INTERNAL is true, or EVENT_ID names them;
  - have not been briefed before (workflow state `briefed`).

For each: attendees → name/org/role (contacts, People), the last threads with
each outside attendee (both directions), Teams chats with them, the meeting's
attached docs (read), files named like the meeting, and GitHub issues/PRs
that mention their organisation within my scope.

Exit 10 = no meeting needs a brief right now (not an error; the run ends).
Exit 3  = no calendar could be read at all.
"""
import datetime as dt
import json
import os
import re
import sys

import sources as S

now = S.now_utc()
f_min, t_min = float(os.environ.get("FROM_MIN") or 25), float(os.environ.get("TO_MIN") or 40)
event_id = os.environ.get("EVENT_ID", "").strip()
incl_int = os.environ.get("INCLUDE_INTERNAL", "false") == "true"
kw = [k.strip().lower() for k in os.environ.get("KEYWORDS", "").split(",") if k.strip()]
cap = int(os.environ.get("MAX_MEETINGS") or 3)

accts = S.accounts(os.environ.get("ACCOUNTS", ""))
tmin, tmax = now + dt.timedelta(minutes=f_min), now + dt.timedelta(minutes=t_min)
E = S.Evidence("meeting-brief", {"from": S.iso(tmin), "to": S.iso(tmax)})
internal = S.internal_domains(accts, os.environ.get("INTERNAL_DOMAINS", ""))
me = {a["email"] for a in accts}
E.doc["me"] = {"accounts": [a["handle"] for a in accts], "emails": sorted(me), "internal_domains": sorted(internal)}
try:
    briefed = json.loads(S.wfx_state("workflow", "briefed") or "{}")
except json.JSONDecodeError:
    briefed = {}

cal = {}
for a, refs in zip(accts, S.pmap(lambda a: S.calendar(E, a, tmin, tmax), accts, 4)):
    cal[a["handle"]] = refs

picked, skipped = [], []
for h, refs in cal.items():
    for ref in refs:
        ev = E.doc["items"][ref]
        ev.update(S.meeting_class(ev, internal, me))
        start = S.parse_time(ev.get("start"))
        why = None
        if event_id and event_id not in ref:
            continue
        if ev.get("all_day") or not ev.get("others"):
            skipped.append((ref, "no other attendees / all-day"))
            continue
        if start and not event_id and not (tmin <= start <= tmax):
            continue
        if ref in briefed and not event_id:
            skipped.append((ref, "already briefed"))
            continue
        if event_id:
            why = "requested"
        elif ev.get("external"):
            why = "external: " + ", ".join(ev["external_domains"])
        elif kw and any(k in ev.get("title", "").lower() for k in kw):
            why = "keyword"
        elif incl_int:
            why = "internal (include_internal)"
        if not why:
            skipped.append((ref, "internal and not flagged important"))
            continue
        ev["brief_reason"] = why
        picked.append((h, ref))
picked = picked[:cap]
E.doc["meetings"] = [r for _, r in picked]
E.doc["skipped"] = [{"ref": r, "why": w} for r, w in skipped]

by_h = {a["handle"]: a for a in accts}
gh_ids = S.gh_identities(os.environ.get("GH_IDENTITIES", ""))
for h, ref in picked:
    a = by_h[h]
    ev = E.doc["items"][ref]
    others = [x for x in ev.get("attendees", []) if x.get("email") and x["email"] not in me
              and "resource.calendar" not in x["email"]][:8]
    ev["people"] = [S.person(E, accts, x["email"]) for x in others]
    outside = [x["email"] for x in others if not S.is_internal(x["email"], internal)] or \
              [x["email"] for x in others]
    ev["threads"] = []
    for email in outside[:3]:
        for acc in accts:  # every mailbox: the thread may live in any of them
            ev["threads"] += S.mail_with(E, acc, email, days=180, cap=4)
    for acc in accts:
        if acc["handle"].startswith("ms:") and outside:
            ev["chats"] = S.chats(E, acc, now - dt.timedelta(days=30), only_with=outside[0], include_mine=True,
                                  name=f"teams-chat CHAT-1+5 with {outside[0]} {acc['handle']}")
    docs = []
    for att in ev.get("attachments", []):
        if att.get("file_id"):
            txt, err = S.doc_text(h, att["file_id"], cap=8000)
            docs.append(E.add(f"file:{h}:{att['file_id']}", "file", account=h, title=att.get("title"),
                              url=att.get("url"), text=txt[:3000] if txt else "",
                              unreadable=err[:120] if err and not txt else None))
    ev["docs"] = docs + S.drive_search(E, a, ev.get("title", ""), cap=4,
                                        name=f"drive DRIVE-14 for '{ev.get('title','')[:30]}' {h}")
    orgs = sorted({S.domain_of(e).split(".")[0] for e in outside if not S.is_internal(e, internal)})
    ev["work"] = []
    for ident in gh_ids:
        login, err = S.github_login(ident)
        if err:
            E.source(f"github {ident}", "unreadable", reason=err, fix=f"apl login {ident}")
            continue
        scope = S.github_scope(login, os.environ.get("GH_SCOPE", ""), ident)
        for org in orgs[:2]:
            ev["work"] += S.gh_search(E, f"{org} updated:>={(now - dt.timedelta(days=60)).date()} {scope}",
                                      f"issues mentioning {org}", cap=5, kind="issues", ident=ident)
        words = [w for w in re.findall(r"[A-Za-z][A-Za-z0-9\-]{3,}", ev.get("title", ""))
                 if w.lower() not in {"meeting", "sync", "call", "weekly", "daily", "with", "review"}][:2]
        if words:
            ev["work"] += S.gh_search(E, f"{' '.join(words)} is:open {scope}", f"PRs about '{' '.join(words)}'",
                                      cap=5, kind="prs", ident=ident)

E.dump("evidence.json")
E.digest("evidence.md")
for s in E.doc["sources"]:
    print(f"{s['status']:10} {s['count']:4}  {s['name']}" + (f"  → {s.get('fix','')}" if s['status'] != 'ok' else ""))
print(f"meetings picked: {len(picked)}  skipped: {len(skipped)}  items: {len(E.doc['items'])}")
for _, r in picked:
    ev = E.doc["items"][r]
    print(f"  → {ev.get('start')} {ev.get('title','')[:60]} ({ev.get('brief_reason')})")
cal_ok = [s for s in E.doc["sources"] if s["name"].startswith("calendar") and s["status"] == "ok"]
if not cal_ok:
    sys.exit(3)
sys.exit(0 if picked else 10)
