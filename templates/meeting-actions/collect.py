#!/usr/bin/env python3
"""collect.py — meetings that just ended, and their notes, read-only.

Sweeps every connected calendar for meetings that ENDED in the last HOURS
hours (default 3) with someone other than me in them and not yet processed
(workflow state `processed`). For each, finds its notes through apl-skill:
  Google: the invite's attachments (Gemini "Notes by …" docs) exported as
          text, else Drive docs named after the meeting (DRIVE-14);
  Microsoft: MEET-2 (resolve by JoinWebUrl) → MEET-6/MEET-7 (transcript VTT)
          and MEET-4 → CHAT-5 (the meeting chat).
Each notes text lands in ./notes/<ref>.txt and is an evidence item the
extracted decisions and actions must QUOTE.

Also reads the attendees (contacts/People) so owners resolve to people.

Exit 10 = no ended meeting has notes (the result lists what was tried).
Exit 3  = no calendar readable.
"""
import datetime as dt
import json
import os
import sys

import sources as S

now = S.now_utc()
hours = float(os.environ.get("HOURS") or 3)
event_id = os.environ.get("EVENT_ID", "").strip()
cap = int(os.environ.get("MAX_MEETINGS") or 3)
accts = S.accounts(os.environ.get("ACCOUNTS", ""))
t0 = now - dt.timedelta(hours=hours)
E = S.Evidence("meeting-actions", {"from": S.iso(t0), "to": S.iso(now)})
internal = S.internal_domains(accts, os.environ.get("INTERNAL_DOMAINS", ""))
me = {a["email"] for a in accts}
E.doc["me"] = {"accounts": [a["handle"] for a in accts], "emails": sorted(me)}
try:
    done = json.loads(S.wfx_state("workflow", "processed") or "{}")
except json.JSONDecodeError:
    done = {}

which = os.environ.get("SOURCES") or "all"   # all | calendar | drive
cands = []
cal_accts = accts if which in ("all", "calendar") else []
for a, refs in zip(cal_accts, S.pmap(lambda a: S.calendar(E, a, t0 - dt.timedelta(hours=4), now), cal_accts, 4)):
    for ref in refs:
        ev = E.doc["items"][ref]
        ev.update(S.meeting_class(ev, internal, me))
        end = S.parse_time(ev.get("end"))
        if event_id:
            if event_id in ref:
                cands.append(ref)
            continue
        if ev.get("all_day") or not ev.get("others") or not end or not (t0 <= end <= now):
            continue
        if ref in done:
            continue
        cands.append(ref)

# Ad-hoc meetings have no calendar event but still leave notes: sweep Drive
# for Gemini notes / Meet chat transcripts created in the window (DRIVE-14),
# newest first, each treated as its own meeting.
adhoc = []
if which in ("all", "drive"):
    for a in accts:
        if not a["handle"].startswith("google:"):
            continue
        name = f"drive DRIVE-14 meeting notes {a['handle']}"
        if not S.can(E, a, "drive", name):
            continue
        q = ("(name contains 'Notes by Gemini' or name contains 'Chat transcript' or name contains 'Transcript') "
             f"and modifiedTime > '{S.iso(t0)[:19]}' and trashed = false")
        d, err = S._apl(a["handle"], f"{S.GDRIVE}/files?pageSize=10&orderBy=modifiedTime%20desc&q={S.up.quote(q)}"
                                     "&fields=files(id,name,mimeType,modifiedTime,createdTime,webViewLink)")
        if err:
            E.source(name, "unreadable", reason=err, fix=S.fix_for(a["handle"], err))
            continue
        files = (d or {}).get("files", [])
        E.source(name, "ok", len(files))
        for f in files:
            ref = f"event:{a['handle']}:notes-{f['id']}"
            if ref in done and not event_id:
                continue
            if event_id and event_id not in ref and event_id != f["id"]:
                continue
            E.add(ref, "event", account=a["handle"], title=f["name"][:140], start=f.get("createdTime"),
                  end=f.get("modifiedTime"), url=f.get("webViewLink"), adhoc=True,
                  attachments=[{"title": f["name"], "file_id": f["id"], "url": f.get("webViewLink")}])
            adhoc.append(ref)
cands = sorted(cands, key=lambda r: E.doc["items"][r].get("end", ""), reverse=True) + adhoc

with_notes, without = [], []
for ref in cands:
    notes = S.meeting_notes(E, ref, "notes")
    ev = E.doc["items"][ref]
    if notes:
        ev["notes"] = notes
        with_notes.append(ref)
        ev["people"] = [S.person(E, accts, x["email"]) for x in ev.get("attendees", [])
                        if x.get("email") and "resource.calendar" not in x["email"]][:10]
    else:
        without.append({"ref": ref, "title": ev.get("title"), "end": ev.get("end"),
                        "tried": ev.get("notes_tried", [])})
    if len(with_notes) >= cap:
        break
E.doc["meetings"] = with_notes
E.doc["without_notes"] = without
for w in without:
    E.source(f"notes for '{(w['title'] or '')[:40]}'", "empty",
             reason="no notes, transcript or meeting chat found (tried: " + ", ".join(w["tried"] or ["nothing to try"]) + ")",
             fix="turn on Gemini notes / Teams transcription, or attach notes to the invite")

E.dump("evidence.json")
E.digest("evidence.md")
for s in E.doc["sources"]:
    print(f"{s['status']:10} {s['count']:4}  {s['name']}" + (f"  → {s.get('reason','')}" if s['status'] != 'ok' else ""))
print(f"ended meetings: {len(cands)}  with notes: {len(with_notes)}  without: {len(without)}")
for r in with_notes:
    ev = E.doc["items"][r]
    print(f"  → {ev.get('end')} {ev.get('title','')[:60]}: {[E.doc['items'][n]['via'] for n in ev['notes']]}")
if not [s for s in E.doc["sources"] if s["name"].startswith("calendar") and s["status"] == "ok"]:
    sys.exit(3)
sys.exit(0 if with_notes else 10)
