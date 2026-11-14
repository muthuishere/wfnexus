#!/usr/bin/env python3
"""apl_cmd.py — the apl module's thin command wrappers (#109).

    apl_cmd.py plan <command>   a sending command: apply the response policy
    apl_cmd.py act  <command>   do it through `apl` and print ONE JSON result

Inputs arrive as files in the step's workspace: in.<name> (one per declared
input; the step writes them with quoted heredocs, so text is never shell).

Every call goes through `apl` ($APL_BIN, default `apl`): `apl call <handle>
<METHOD> <url>` for Google/Microsoft APIs, `apl find` and `apl contacts
resolve` for search. The handle is the `handle` input, else the profile's
`<kind>_handle` input (mail_handle, teams_handle, calendar_handle,
drive_handle), else the profile's account.

plan exit codes: 0 auto, 10 approval (the engine's pause already asked), 11 draft, 12 none, 2 bad
input. A drafted or skipped send prints {"status": "drafted"|"skipped", ...}
and touches nothing.
"""
import base64
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

GRAPH = "https://graph.microsoft.com/v1.0"
GMAIL = "https://gmail.googleapis.com/gmail/v1/users/me"
GCAL = "https://www.googleapis.com/calendar/v3/calendars/primary"
GDRIVE = "https://www.googleapis.com/drive/v3/files"
GUPLOAD = "https://www.googleapis.com/upload/drive/v3/files"

# command → (kind of handle, the inputs that must be non-empty)
COMMANDS = {
    "mail-send": ("mail", ["to", "subject", "body"]),
    "mail-draft": ("mail", ["to", "subject", "body"]),
    "mail-list": ("mail", []),
    "teams-send": ("teams", ["chat", "text"]),
    "calendar-list": ("calendar", []),
    "calendar-create": ("calendar", ["subject", "start", "end"]),
    "drive-get": ("drive", ["path", "out"]),
    "drive-put": ("drive", ["path", "file"]),
    "contacts-find": (None, ["name"]),
    "find": (None, []),
}
POLICY = {"auto": 0, "approval": 10, "draft": 11, "none": 12}


def inp(name, default=""):
    try:
        return open("in." + name).read().rstrip("\n")
    except FileNotFoundError:
        return default


def out(res, code=0):
    print(json.dumps(res))
    sys.exit(code)


def apl(*args, body=None):
    cmd = [os.environ.get("APL_BIN") or "apl", *args]
    if body is not None:
        cmd += ["--body-file", "-"]
    r = subprocess.run(cmd, input=body, capture_output=True, text=True, timeout=120)
    if r.returncode != 0:
        out({"status": "failed", "error": (r.stderr or r.stdout).strip()[-500:], "command": args[:3]}, 1)
    try:
        return json.loads(r.stdout) if r.stdout.strip() else {}
    except ValueError:
        return {"raw": r.stdout[-4000:]}


def profile():
    # The engine hands the step the profile it resolved (for a write, the one
    # shown and pinned when it was approved); the step never looks it up.
    pinned = os.environ.get("WFX_MODULE_PROFILE")
    if pinned is not None:
        try:
            p = json.loads(pinned)
        except ValueError:
            return {}
        return {} if p.get("error") else p
    api, tok = os.environ.get("WFX_API", "").rstrip("/"), os.environ.get("WFX_MODULE_TOKEN", "")
    if not api or not tok:
        return {}
    url = api + "/api/module-self/" + os.environ.get("WFX_MODULE", "apl") + "/profile?" + \
        urllib.parse.urlencode({"name": inp("profile")})
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + tok})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return json.loads(r.read())
    except Exception:
        return {}


def handle_for(kind):
    h = inp("handle").strip()
    if h:
        return h
    p = profile()
    return ((p.get("inputs") or {}).get(kind + "_handle") or p.get("account") or "").strip()


def ms(h):
    return h.startswith("ms:")


def plan(cmd):
    _, need = COMMANDS[cmd]
    bad = [n + " is required" for n in need if not inp(n).strip()]
    pol = inp("responsePolicy", "draft").strip() or "draft"
    if pol not in POLICY:
        bad.append("responsePolicy is draft, approval, auto or none")
    if bad:
        print("invalid: " + "; ".join(bad))
        sys.exit(2)
    print(json.dumps({"command": cmd, "policy": pol}))
    sys.exit(POLICY[pol])


def act(cmd):
    kind, need = COMMANDS[cmd]
    bad = [n + " is required" for n in need if not inp(n).strip()]
    if bad:
        out({"status": "failed", "error": "; ".join(bad)}, 2)
    pol = inp("responsePolicy", "draft").strip() or "draft"
    gated = cmd in ("mail-send", "teams-send", "calendar-create", "drive-put")
    if gated and pol in ("draft", "none"):
        out({"status": "drafted" if pol == "draft" else "skipped", "command": cmd,
             "request": {n: inp(n) for n in need}})
    h = handle_for(kind) if kind else ""
    if kind and not h:
        out({"status": "failed", "error": "no handle: pass handle=, or set the profile's %s_handle" % kind}, 2)
    return globals()["do_" + cmd.replace("-", "_")](h)


def rfc822(to, subject, body):
    msg = "To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s" % (to, subject, body)
    return base64.urlsafe_b64encode(msg.encode()).decode()


def ms_message(to, subject, body):
    return {"subject": subject, "body": {"contentType": "Text", "content": body},
            "toRecipients": [{"emailAddress": {"address": a.strip()}} for a in to.split(",") if a.strip()]}


def do_mail_send(h):
    to, subj, body = inp("to"), inp("subject"), inp("body")
    if ms(h):
        apl("call", h, "POST", GRAPH + "/me/sendMail", body=json.dumps({"message": ms_message(to, subj, body)}))
    else:
        apl("call", h, "POST", GMAIL + "/messages/send", body=json.dumps({"raw": rfc822(to, subj, body)}))
    out({"status": "sent", "command": "mail-send", "to": to, "handle": h})


def do_mail_draft(h):
    to, subj, body = inp("to"), inp("subject"), inp("body")
    if ms(h):
        r = apl("call", h, "POST", GRAPH + "/me/messages", body=json.dumps(ms_message(to, subj, body)))
    else:
        r = apl("call", h, "POST", GMAIL + "/drafts", body=json.dumps({"message": {"raw": rfc822(to, subj, body)}}))
    out({"status": "drafted", "command": "mail-draft", "id": r.get("id", ""), "handle": h})


def do_mail_list(h):
    q, top = inp("query"), inp("top", "10") or "10"
    if ms(h):
        url = GRAPH + "/me/messages?" + urllib.parse.urlencode(
            {"$top": top, "$orderby": "receivedDateTime desc", "$select": "subject,from,receivedDateTime,isRead"})
        if q:
            url += "&" + urllib.parse.urlencode({"$search": '"%s"' % q})
        r = apl("call", h, "GET", url)
        items = [{"id": m.get("id"), "subject": m.get("subject"), "from": ((m.get("from") or {}).get("emailAddress") or {}).get("address"),
                  "at": m.get("receivedDateTime")} for m in r.get("value", [])]
    else:
        r = apl("call", h, "GET", GMAIL + "/messages?" + urllib.parse.urlencode({"maxResults": top, "q": q}))
        items = [{"id": m.get("id")} for m in r.get("messages", [])]
    out({"status": "ok", "command": "mail-list", "handle": h, "messages": items})


def do_teams_send(h):
    chat, text = inp("chat"), inp("text")
    r = apl("call", h, "POST", GRAPH + "/chats/%s/messages" % urllib.parse.quote(chat, safe=""),
            body=json.dumps({"body": {"content": text}}))
    out({"status": "sent", "command": "teams-send", "id": r.get("id", ""), "chat": chat, "handle": h})


def window():
    now = datetime.now(timezone.utc)
    days = int(inp("days", "1") or "1")
    return now.strftime("%Y-%m-%dT%H:%M:%SZ"), (now + timedelta(days=days)).strftime("%Y-%m-%dT%H:%M:%SZ")


def do_calendar_list(h):
    a, b = window()
    if ms(h):
        r = apl("call", h, "GET", GRAPH + "/me/calendarView?" + urllib.parse.urlencode(
            {"startDateTime": a, "endDateTime": b, "$select": "subject,start,end,organizer", "$orderby": "start/dateTime"}))
        ev = [{"id": e.get("id"), "subject": e.get("subject"), "start": (e.get("start") or {}).get("dateTime"),
               "end": (e.get("end") or {}).get("dateTime")} for e in r.get("value", [])]
    else:
        r = apl("call", h, "GET", GCAL + "/events?" + urllib.parse.urlencode(
            {"timeMin": a, "timeMax": b, "singleEvents": "true", "orderBy": "startTime"}))
        ev = [{"id": e.get("id"), "subject": e.get("summary"), "start": (e.get("start") or {}).get("dateTime"),
               "end": (e.get("end") or {}).get("dateTime")} for e in r.get("items", [])]
    out({"status": "ok", "command": "calendar-list", "handle": h, "events": ev})


def do_calendar_create(h):
    subj, start, end = inp("subject"), inp("start"), inp("end")
    att = [a.strip() for a in inp("attendees").split(",") if a.strip()]
    if ms(h):
        r = apl("call", h, "POST", GRAPH + "/me/events", body=json.dumps({
            "subject": subj, "start": {"dateTime": start, "timeZone": "UTC"}, "end": {"dateTime": end, "timeZone": "UTC"},
            "attendees": [{"emailAddress": {"address": a}, "type": "required"} for a in att]}))
    else:
        r = apl("call", h, "POST", GCAL + "/events", body=json.dumps({
            "summary": subj, "start": {"dateTime": start}, "end": {"dateTime": end},
            "attendees": [{"email": a} for a in att]}))
    out({"status": "created", "command": "calendar-create", "id": r.get("id", ""), "handle": h})


def do_drive_get(h):
    path, dest = inp("path"), inp("out")
    if ms(h):
        url = GRAPH + "/me/drive/root:/%s:/content" % urllib.parse.quote(path.lstrip("/"))
    else:
        url = GDRIVE + "/%s?alt=media" % urllib.parse.quote(path, safe="")
    apl("call", h, "GET", url, "-o", dest)
    out({"status": "ok", "command": "drive-get", "path": path, "out": dest, "handle": h})


def do_drive_put(h):
    path, src = inp("path"), inp("file")
    if ms(h):
        r = apl("call", h, "PUT", GRAPH + "/me/drive/root:/%s:/content" % urllib.parse.quote(path.lstrip("/")), "--body-file", src)
    else:
        r = apl("call", h, "POST", GUPLOAD + "?uploadType=media", "--body-file", src)
    out({"status": "uploaded", "command": "drive-put", "id": r.get("id", ""), "path": path, "handle": h})


def do_contacts_find(_):
    r = apl("contacts", "resolve", inp("name"))
    out({"status": "ok", "command": "contacts-find", "result": r})


def do_find(_):
    args = ["find"]
    if inp("query"):
        args.append(inp("query"))
    if inp("from"):
        args += ["--from", inp("from")]
    if inp("only"):
        args += ["--only", inp("only")]
    args += ["--limit", inp("limit", "10") or "10", "--json"]
    r = apl(*args)
    out({"status": "ok", "command": "find", "results": r})


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("plan", "act") or sys.argv[2] not in COMMANDS:
        print("usage: apl_cmd.py plan|act <%s>" % "|".join(COMMANDS))
        sys.exit(2)
    (plan if sys.argv[1] == "plan" else act)(sys.argv[2])
