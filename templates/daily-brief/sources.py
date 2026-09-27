#!/usr/bin/env python3
"""sources.py — READ-ONLY evidence collection for the leadership templates.

The same file ships beside every leadership template (daily-brief,
meeting-brief, meeting-actions, weekly-leadership-digest, decision-log,
one-on-one-prep) so each template directory stays self-contained when
`wfx new` copies it.

Rules this file enforces, so no prompt has to:

* THE apl-skill IS THE BACKBONE. Every collector below is one of the skill's
  named recipes (~/.claude/skills/apl-skill/references/*.md) — the recipe id
  is in the function's docstring and in the source name the brief prints —
  run the way the skill runs it: `apl call <handle> GET …`, the skill's own
  `bin/apl-mail-brief` for the unread sweep (MAIL-X-3), `apl features` for
  the scope preflight (MAIL-X-1 prerequisites), and `apl with github:<label>
  -- gh …` for every GitHub identity the owner has bound (github.md).
  Recipes are extended only where a brief needs more: a time window, a few
  more $select fields, To/Cc headers.
* GET ONLY. Every mailbox / calendar / chat / drive call is `apl call … GET`;
  GitHub is `gh search|pr|issue … --json` and `gh api -X GET`.
  There is no code path here that sends, replies, accepts, labels, moves
  or deletes anything. `_apl` refuses any other verb.
* Every item gets a stable REF (`mail:google:work:<id>`, `event:…`,
  `pr:owner/repo#12`, …). Briefs cite refs; verify.py rejects a line whose
  refs are not in evidence.json.
* A source that could not be read is RECORDED with the reason and the exact
  command that fixes it (`apl login google:work --force`). Nothing fails
  silently: the brief prints the list.
* Tokens never pass through here — apl injects the bearer itself.
"""
from __future__ import annotations

import concurrent.futures as cf
import datetime as dt
import html
import json
import os
import re
import subprocess
import sys
import time
import urllib.parse as up

GMAIL = "https://gmail.googleapis.com/gmail/v1/users/me"
GCAL = "https://www.googleapis.com/calendar/v3/calendars/primary/events"
GDRIVE = "https://www.googleapis.com/drive/v3"
GTASKS = "https://tasks.googleapis.com/tasks/v1"
GPEOPLE = "https://people.googleapis.com/v1"
GRAPH = "https://graph.microsoft.com/v1.0"
PUBLIC_MAIL = {"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com",
               "yahoo.com", "icloud.com", "me.com", "proton.me", "protonmail.com"}
BOT_SENDER = re.compile(r"no-?reply|do-?not-?reply|notifications?@|mailer-daemon|noreply|"
                        r"alerts?@|digest@|newsletter|updates@|billing@|receipts?@|"
                        r"calendar-notification|@github\.com|@linkedin\.com", re.I)


def now_utc() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def iso(t: dt.datetime) -> str:
    return t.astimezone(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_time(s: str | None) -> dt.datetime | None:
    if not s:
        return None
    s = s.strip()
    try:
        if len(s) == 10:  # all-day date
            return dt.datetime.fromisoformat(s).replace(tzinfo=dt.timezone.utc)
        s = s.replace("Z", "+00:00")
        # Graph gives 7 fractional digits and no offset
        m = re.match(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?([+-]\d\d:\d\d)?$", s)
        if m:
            base = dt.datetime.fromisoformat(m.group(1))
            tz = m.group(3)
            if tz:
                return dt.datetime.fromisoformat(m.group(1) + tz)
            return base.replace(tzinfo=dt.timezone.utc)
        return dt.datetime.fromisoformat(s)
    except ValueError:
        return None


def strip_html(s: str | None, cap: int = 600) -> str:
    if not s:
        return ""
    s = re.sub(r"(?is)<(script|style).*?</\1>", " ", s)
    s = re.sub(r"(?s)<[^>]+>", " ", s)
    s = html.unescape(s)
    s = re.sub(r"\s+", " ", s).strip()
    return s[:cap]


def sid(long_id: str) -> str:
    """Graph ids are long and share prefixes/suffixes across occurrences; a
    short stable hash keeps refs unique and readable."""
    import hashlib
    return hashlib.sha1((long_id or "").encode()).hexdigest()[:16]


def domain_of(email: str) -> str:
    return email.rsplit("@", 1)[-1].lower() if "@" in (email or "") else ""


def addr_of(header: str) -> str:
    m = re.search(r"<([^>]+)>", header or "")
    return (m.group(1) if m else (header or "")).strip().lower()


# ─────────────────────────────────────────────────────────────── the record
class Evidence:
    """Everything a brief may cite, plus the honest list of what was not read."""

    def __init__(self, purpose: str, window: dict):
        self.doc = {"purpose": purpose, "generated_at": iso(now_utc()), "window": window,
                    "me": {}, "sources": [], "items": {}}

    def add(self, ref: str, kind: str, **fields) -> str:
        fields = {k: v for k, v in fields.items() if v not in (None, "", [], {})}
        self.doc["items"][ref] = {"kind": kind, **fields}
        return ref

    def source(self, name: str, status: str, count: int = 0, reason: str = "", fix: str = ""):
        row = {"name": name, "status": status, "count": count}
        if reason:
            row["reason"] = reason[:300]
        if fix:
            row["fix"] = fix
        self.doc["sources"].append(row)

    def dump(self, path: str):
        with open(path, "w") as f:
            json.dump(self.doc, f, indent=1, default=str)

    def digest(self, path: str, extra_order: list[str] | None = None):
        """A compact, one-line-per-item view for the drafting agent."""
        lines = [f"# evidence digest — {self.doc['purpose']}",
                 f"window: {json.dumps(self.doc['window'])}",
                 f"me: {json.dumps(self.doc['me'])}", "", "## sources"]
        for s in self.doc["sources"]:
            lines.append(f"- {s['name']}: {s['status']} ({s['count']})"
                         + (f" — {s.get('reason','')} FIX: {s.get('fix','')}" if s["status"] != "ok" else ""))
        lines += ["", "## items (cite the ref in [brackets] exactly)"]
        for ref, it in self.doc["items"].items():
            lines.append(f"[{ref}] " + json.dumps({k: v for k, v in it.items() if k != "body"},
                                                   ensure_ascii=False, default=str)[:900])
        with open(path, "w") as f:
            f.write("\n".join(lines) + "\n")


def fix_for(handle: str, err: str) -> str:
    e = err.lower()
    m = re.search(r"https://console\.developers\.google\.com/apis/api/[^\s]+", err)
    if m or "has not been used in project" in e or "it is disabled" in e:
        return "enable this API in the Google Cloud project behind `apl setup google`" + (f": {m.group(0)}" if m else "")
    if handle.startswith("gh"):
        return "gh auth login"
    if "403" in e or "insufficient" in e or "forbidden" in e or "scope" in e or "accessdenied" in e:
        return f"apl login {handle} --force  (grant the missing scope; admin consent may be needed)"
    return f"apl login {handle} --force"


# ─────────────────────────────────────────────────────────────── transports
def _apl(handle: str, url: str, headers: list[str] | None = None, timeout: int = 60, raw: bool = False):
    """GET through apl. Returns (data, error). Never any other verb."""
    cmd = ["apl", "call", handle, "GET", url, "--timeout", f"{timeout}s"]
    for h in headers or []:
        cmd += ["-H", h]
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout + 15)
    except (subprocess.TimeoutExpired, FileNotFoundError) as ex:
        return None, f"{type(ex).__name__}: {ex}"[:200]
    if raw:
        if p.returncode != 0:
            return None, (p.stderr or p.stdout).strip()[:300]
        return p.stdout, None
    body = (p.stdout or "").strip()
    try:
        data = json.loads(body) if body else None
    except json.JSONDecodeError:
        data = None
    if isinstance(data, dict) and "error" in data:
        e = data["error"]
        if isinstance(e, dict):
            return None, f"{e.get('code','')} {e.get('status','')} {e.get('message','')}".strip()[:300]
        return None, str(e)[:300]
    if p.returncode != 0 and data is None:
        return None, (p.stderr or body or f"exit {p.returncode}").strip()[:300]
    return data, None


def gh_identities(only: str = "") -> list[str]:
    """Every `github:<label>` bound in apl (github.md: route each login through
    `apl with github:<label>`), optionally narrowed; [""] = plain gh."""
    want = {x.strip() for x in only.split(",") if x.strip()}
    try:
        p = subprocess.run(["apl", "identity", "list"], capture_output=True, text=True, timeout=30)
        ids = [l.split()[0] for l in p.stdout.splitlines() if l.startswith("github:")]
    except Exception:
        ids = []
    if want:
        ids = [i for i in ids if i in want or i.split(":", 1)[1] in want]
    return ids or [""]


def _ghx(ident: str, args: list[str], timeout: int = 90):
    """gh as one identity: `apl with github:<label> -- gh …` (never `gh auth switch`)."""
    cmd = (["apl", "with", ident, "--"] if ident else []) + ["gh", *args]
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except (subprocess.TimeoutExpired, FileNotFoundError) as ex:
        return None, f"{type(ex).__name__}"
    if p.returncode != 0:
        return None, (p.stderr or p.stdout).strip()[:300]
    try:
        return json.loads(p.stdout), None
    except json.JSONDecodeError:
        return None, "unparseable gh output"


def _gh(path: str, fields: dict | None = None, timeout: int = 60, ident: str = ""):
    """github.md fallback: `gh api -X GET <path>` (read-only REST)."""
    args = ["api", "-X", "GET", path]
    for k, v in (fields or {}).items():
        args += ["-f", f"{k}={v}"]
    return _ghx(ident, args, timeout)


def pmap(fn, xs, workers: int = 8):
    xs = list(xs)
    if not xs:
        return []
    with cf.ThreadPoolExecutor(max_workers=min(workers, len(xs))) as ex:
        return list(ex.map(fn, xs))


# ─────────────────────────────────────────────────────────────── accounts
def accounts(only: str = "") -> list[dict]:
    """Every google:/ms: handle apl holds, optionally narrowed (comma list)."""
    try:
        p = subprocess.run(["apl", "accounts", "--json"], capture_output=True, text=True, timeout=30)
        rows = json.loads(p.stdout or "[]")
    except Exception:
        rows = []
    want = {x.strip() for x in only.split(",") if x.strip()}
    out = []
    for a in rows:
        h = a.get("handle", "")
        if not (h.startswith("google:") or h.startswith("ms:")):
            continue
        if want and h not in want:
            continue
        out.append({"handle": h, "email": (a.get("email") or "").lower(), "provider": a.get("provider"),
                    "scopes": a.get("scopes") or []})
    return out


FAMILY_SCOPES = {  # any one alias is enough; the first is what the skill asks for
    ("google", "mail"): ["gmail.modify", "gmail.readonly"],
    ("ms", "mail"): ["Mail.Read", "Mail.ReadWrite"],
    ("google", "calendar"): ["calendar.readonly", "calendar"],
    ("ms", "calendar"): ["Calendars.Read", "Calendars.ReadWrite"],
    ("ms", "chat"): ["Chat.Read", "Chat.ReadWrite"],
    ("google", "drive"): ["drive.readonly", "drive"],
    ("ms", "drive"): ["Files.Read.All", "Files.ReadWrite.All", "Files.Read", "Files.ReadWrite"],
    ("google", "contacts"): ["contacts.readonly", "contacts.other.readonly"],
    ("ms", "contacts"): ["People.Read", "Contacts.Read"],
    ("google", "tasks"): ["tasks.readonly", "tasks"],
    ("ms", "tasks"): ["Tasks.Read", "Tasks.ReadWrite"],
    ("ms", "transcripts"): ["OnlineMeetingTranscript.Read.All"],
}
_FEATURES: dict[str, set[str]] = {}


def features(h: str) -> set[str]:
    """`apl features <handle> --json` — the skill's MAIL-X-1 prerequisite check."""
    if h not in _FEATURES:
        try:
            p = subprocess.run(["apl", "features", h, "--json"], capture_output=True, text=True, timeout=30)
            _FEATURES[h] = {x["alias"] for x in json.loads(p.stdout or "[]") if x.get("available")}
        except Exception:
            _FEATURES[h] = set()
    return _FEATURES[h]


def can(E: "Evidence", a: dict, family: str, name: str) -> bool:
    """True when the handle holds a scope for this family; otherwise the gap is
    recorded with the exact `apl login … --scope` the owner must run."""
    prov = "google" if a["handle"].startswith("google:") else "ms"
    need = FAMILY_SCOPES.get((prov, family))
    if not need:
        return True
    have = features(a["handle"])
    if not have or any(s in have for s in need):
        return True  # empty = features unknown: try, and let the call say
    E.source(name, "unreadable", reason=f"scope not granted ({' or '.join(need)})",
             fix=f"apl login {a['handle']} --force --scope {need[0]}")
    return False


def internal_domains(accts: list[dict], override: str = "") -> set[str]:
    if override.strip():
        return {d.strip().lower() for d in override.split(",") if d.strip()}
    out = set()
    for a in accts:
        d = domain_of(a["email"])
        if not d or d in PUBLIC_MAIL:
            continue
        out.add(d)
        if d.endswith(".onmicrosoft.com"):
            out.add(d.split(".")[0] + ".com")
    return out


def is_internal(email: str, internal: set[str]) -> bool:
    d = domain_of(email)
    if not d:
        return True
    return any(d == x or d.endswith("." + x) for x in internal)


_LOGIN: dict[str, str] = {}


def github_login(ident: str = "") -> tuple[str, str | None]:
    if ident in _LOGIN:
        return _LOGIN[ident], None
    d, err = _gh("user", ident=ident)
    login = (d or {}).get("login", "")
    if login:
        _LOGIN[ident] = login
    return login, err


def github_scope(login: str, override: str = "", ident: str = "") -> str:
    """`user:me org:a org:b` — GitHub ORs repeated user:/org: qualifiers."""
    if override.strip():
        return override.strip()
    orgs, _ = _gh("user/orgs", {"per_page": "100"}, ident=ident)
    parts = [f"user:{login}"] + [f"org:{o['login']}" for o in (orgs or [])]
    return " ".join(parts)


# ─────────────────────────────────────────────────────────────── calendar
def calendar(E: Evidence, a: dict, tmin: dt.datetime, tmax: dt.datetime, name: str = "calendar") -> list[str]:
    """calendar.md CAL-R-7 (Google) / CAL-R-1+CAL-R-2 (Microsoft calendarView),
    widened to any window and to attendees, attachments and join links."""
    h = a["handle"]
    name = f"{name} {'CAL-R-7' if h.startswith('google:') else 'CAL-R-2'}"
    refs: list[str] = []
    if not can(E, a, "calendar", f"{name} {h}"):
        return refs
    if h.startswith("google:"):
        url = (f"{GCAL}?timeMin={iso(tmin)}&timeMax={iso(tmax)}&singleEvents=true"
               f"&orderBy=startTime&maxResults=100&supportsAttachments=true")
        d, err = _apl(h, url)
        if err:
            E.source(f"{name} {h}", "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for e in d.get("items", []):
            if e.get("status") == "cancelled":
                continue
            atts = e.get("attendees", [])
            mine = next((x for x in atts if x.get("self")), {})
            if mine.get("responseStatus") == "declined":
                continue
            s, en = e.get("start", {}), e.get("end", {})
            refs.append(E.add(
                f"event:{h}:{e['id']}", "event", account=h,
                title=(e.get("summary") or "(no title)")[:140],
                start=s.get("dateTime") or s.get("date"), end=en.get("dateTime") or en.get("date"),
                all_day=bool(s.get("date")), recurring=bool(e.get("recurringEventId")),
                organizer=(e.get("organizer") or {}).get("email", ""),
                attendees=[{"email": x.get("email", "").lower(), "name": x.get("displayName", ""),
                            "response": x.get("responseStatus", ""), "self": bool(x.get("self"))}
                           for x in atts][:40],
                my_response=mine.get("responseStatus", ""),
                location=(e.get("location") or "")[:120], join_url=e.get("hangoutLink", ""),
                url=e.get("htmlLink", ""), description=strip_html(e.get("description"), 800),
                attachments=[{"title": x.get("title", ""), "file_id": x.get("fileId", ""),
                              "url": x.get("fileUrl", ""), "mime": x.get("mimeType", "")}
                             for x in e.get("attachments", [])],
                event_type=e.get("eventType", "")))
    else:
        url = (f"{GRAPH}/me/calendarView?startDateTime={iso(tmin)}&endDateTime={iso(tmax)}&$top=100"
               "&$orderby=start/dateTime&$select=id,subject,start,end,isAllDay,organizer,attendees,"
               "bodyPreview,webLink,onlineMeeting,isCancelled,responseStatus,location,seriesMasterId,type")
        d, err = _apl(h, url, ['Prefer: outlook.timezone="UTC"'])
        if err:
            E.source(f"{name} {h}", "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for e in d.get("value", []):
            if e.get("isCancelled") or (e.get("responseStatus") or {}).get("response") == "declined":
                continue
            refs.append(E.add(
                f"event:{h}:{sid(e['id'])}", "event", account=h, graph_id=e["id"],
                title=(e.get("subject") or "(no title)")[:140],
                start=(e.get("start") or {}).get("dateTime", "")[:19] + "Z",
                end=(e.get("end") or {}).get("dateTime", "")[:19] + "Z",
                all_day=e.get("isAllDay", False), recurring=bool(e.get("seriesMasterId")),
                organizer=((e.get("organizer") or {}).get("emailAddress") or {}).get("address", "").lower(),
                attendees=[{"email": ((x.get("emailAddress") or {}).get("address") or "").lower(),
                            "name": (x.get("emailAddress") or {}).get("name", ""),
                            "response": (x.get("status") or {}).get("response", "")}
                           for x in e.get("attendees", [])][:40],
                my_response=(e.get("responseStatus") or {}).get("response", ""),
                location=((e.get("location") or {}).get("displayName") or "")[:120],
                join_url=(e.get("onlineMeeting") or {}).get("joinUrl", ""),
                url=e.get("webLink", ""), description=(e.get("bodyPreview") or "")[:800]))
    E.source(f"{name} {h}", "ok", len(refs))
    return refs


# ─────────────────────────────────────────────────────────────── mail
def _gmail_meta(h: str, mid: str):
    url = (f"{GMAIL}/messages/{mid}?format=metadata&metadataHeaders=From&metadataHeaders=To"
           "&metadataHeaders=Cc&metadataHeaders=Subject&metadataHeaders=Date")
    return _apl(h, url)


def mail(E: Evidence, a: dict, since: dt.datetime, folder: str = "inbox", query: str = "",
         cap: int = 30, name: str = "") -> list[str]:
    """Mail since a time. folder: inbox | sent | any. Metadata + snippet only."""
    """mail.md MAIL-R-17a/18 (Gmail list + metadata headers) and MAIL-R-2b/4/13
    (Graph inbox / sent), windowed, with To/Cc so "addressed to me" is known."""
    h, me = a["handle"], a["email"]
    rid = ("MAIL-R-18" if folder != "sent" else "MAIL-R-16 in:sent") if h.startswith("google:") else \
          ("MAIL-R-4" if folder != "sent" else "MAIL-R-13")
    name = name or f"mail-{folder} {rid} {h}"
    refs: list[str] = []
    if not can(E, a, "mail", name):
        return refs
    if h.startswith("google:"):
        q = {"inbox": "in:inbox -category:promotions -category:social -category:forums",
             "sent": "in:sent", "any": "-in:spam -in:trash"}[folder]
        q = f"{q} after:{int(since.timestamp())} {query}".strip()
        d, err = _apl(h, f"{GMAIL}/messages?maxResults={cap}&q={up.quote(q)}")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        ids = [m["id"] for m in (d or {}).get("messages", [])]
        metas = pmap(lambda i: _gmail_meta(h, i), ids)
        for mid, (m, e2) in zip(ids, metas):
            if e2 or not m:
                continue
            hd = {x["name"]: x["value"] for x in m.get("payload", {}).get("headers", [])}
            to = hd.get("To", "")
            labels = m.get("labelIds", [])
            refs.append(E.add(
                f"mail:{h}:{mid}", "mail", account=h, folder=folder, thread=m.get("threadId"),
                sender=hd.get("From", "")[:120], to=to[:300], cc=hd.get("Cc", "")[:200],
                subject=hd.get("Subject", "")[:200], date=hd.get("Date", "")[:40],
                ts=iso(dt.datetime.fromtimestamp(int(m.get("internalDate", "0")) / 1000, dt.timezone.utc)),
                snippet=html.unescape(m.get("snippet") or "")[:400],
                unread="UNREAD" in labels, important="IMPORTANT" in labels,
                to_me=me in to.lower(), automated=bool(BOT_SENDER.search(hd.get("From", ""))),
                url=f"https://mail.google.com/mail/u/{me}/#all/{m.get('threadId')}"))
    else:
        box = {"inbox": "mailFolders/inbox/messages", "sent": "mailFolders/sentitems/messages",
               "any": "messages"}[folder]
        field = "sentDateTime" if folder == "sent" else "receivedDateTime"
        url = (f"{GRAPH}/me/{box}?$top={cap}&$orderby={field}%20desc"
               f"&$filter={field}%20ge%20{iso(since)}"
               "&$select=id,subject,from,toRecipients,ccRecipients,receivedDateTime,sentDateTime,"
               "bodyPreview,isRead,importance,conversationId,webLink,inferenceClassification")
        d, err = _apl(h, url)
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for m in (d or {}).get("value", []):
            if folder == "inbox" and m.get("inferenceClassification") == "other":
                continue
            sender = ((m.get("from") or {}).get("emailAddress") or {})
            to = ", ".join(((x.get("emailAddress") or {}).get("address") or "") for x in m.get("toRecipients", []))
            refs.append(E.add(
                f"mail:{h}:{sid(m['id'])}", "mail", account=h, folder=folder, thread=m.get("conversationId"),
                sender=f"{sender.get('name','')} <{sender.get('address','')}>"[:120], to=to[:300],
                subject=(m.get("subject") or "")[:200], ts=m.get(field, ""),
                snippet=(m.get("bodyPreview") or "")[:400], unread=not m.get("isRead", True),
                important=m.get("importance") == "high", to_me=me in to.lower(),
                automated=bool(BOT_SENDER.search(sender.get("address", ""))), url=m.get("webLink", "")))
    E.source(name, "ok", len(refs))
    return refs


APL_SKILL = os.path.expanduser(os.environ.get("APL_SKILL_DIR", "~/.claude/skills/apl-skill"))


def mail_sweep(E: Evidence, handles: list[str], n: int = 20) -> dict[str, int]:
    """mail.md MAIL-X-3: the skill's own one-shot unread sweep, `bin/apl-mail-brief`,
    across every handle. Used as a CROSS-CHECK: its unread count per handle is
    printed beside the brief's, so a collector that silently missed a mailbox
    shows up as a mismatch instead of an empty section."""
    tool = os.path.join(APL_SKILL, "bin", "apl-mail-brief")
    counts: dict[str, int] = {}
    if not os.path.exists(tool):
        E.source("mail MAIL-X-3 apl-mail-brief", "unreadable", reason=f"{tool} not found",
                 fix="npx @deemwarhq/apl-skill install")
        return counts
    try:
        p = subprocess.run([tool, "-n", str(n), *handles], capture_output=True, text=True, timeout=240)
    except subprocess.TimeoutExpired:
        E.source("mail MAIL-X-3 apl-mail-brief", "unreadable", reason="timed out")
        return counts
    errs = []
    for line in p.stdout.splitlines():
        m = re.match(r"^\[([^\]]+)\]\s*(.*)$", line)
        if not m:
            continue
        h, rest = m.group(1), m.group(2)
        counts.setdefault(h, 0)
        if rest.startswith("ERROR"):
            errs.append(f"{h}: {rest[:120]}")
        elif rest != "(inbox clear)":
            counts[h] += 1
    E.doc["unread_sweep"] = counts
    E.source("mail MAIL-X-3 apl-mail-brief", "ok" if not errs else "partial", sum(counts.values()),
             reason="; ".join(errs))
    return counts


def drive_shared(E: Evidence, a: dict, since: dt.datetime, cap: int = 15) -> list[str]:
    """drive.md DRIVE-16 (Google shared-with-me, newest) / DRIVE-2 (OneDrive
    sharedWithMe): documents someone put in front of me in the window."""
    h = a["handle"]
    name = f"drive-shared {'DRIVE-16' if h.startswith('google:') else 'DRIVE-2'} {h}"
    refs: list[str] = []
    if not can(E, a, "drive", name):
        return refs
    if h.startswith("google:"):
        q = f"sharedWithMe and modifiedTime > '{iso(since)[:19]}' and trashed = false"
        d, err = _apl(h, f"{GDRIVE}/files?q={up.quote(q)}&pageSize={cap}&orderBy=modifiedTime%20desc"
                         "&fields=files(id,name,mimeType,modifiedTime,webViewLink,owners(emailAddress),"
                         "sharingUser(emailAddress,displayName))")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for f in (d or {}).get("files", []):
            refs.append(E.add(f"file:{h}:{f['id']}", "file", account=h, title=f["name"][:160],
                              modified=f.get("modifiedTime"), url=f.get("webViewLink"),
                              shared_by=((f.get("sharingUser") or {}).get("emailAddress")
                                         or ((f.get("owners") or [{}])[0]).get("emailAddress", ""))))
    else:
        d, err = _apl(h, f"{GRAPH}/me/drive/sharedWithMe")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for f in (d or {}).get("value", []):
            mod = parse_time(f.get("lastModifiedDateTime") or (f.get("remoteItem") or {}).get("lastModifiedDateTime"))
            if mod and mod < since:
                continue
            by = (((f.get("remoteItem") or {}).get("shared") or {}).get("sharedBy") or {}).get("user") or {}
            refs.append(E.add(f"file:{h}:{sid(f['id'])}", "file", account=h, title=f.get("name", "")[:160],
                              modified=iso(mod) if mod else "", url=f.get("webUrl"),
                              shared_by=by.get("email") or by.get("displayName", "")))
            if len(refs) >= cap:
                break
    E.source(name, "ok", len(refs))
    return refs


def mark_replied(E: Evidence, inbox: list[str], sent: list[str]):
    """A received mail whose thread has a LATER message from me is answered."""
    last_sent: dict[str, str] = {}
    for r in sent:
        it = E.doc["items"][r]
        t = it.get("thread")
        if t and it.get("ts", "") > last_sent.get(t, ""):
            last_sent[t] = it.get("ts", "")
    for r in inbox:
        it = E.doc["items"][r]
        t = it.get("thread")
        it["replied_by_me"] = bool(t and last_sent.get(t, "") > it.get("ts", ""))


def mail_with(E: Evidence, a: dict, email: str, days: int = 120, cap: int = 6) -> list[str]:
    """The last threads with one person, both directions — mail.md MAIL-R-19 /
    MAIL-W-0 step 5 (Gmail from:/to:) and MAIL-R-5 ($search, Graph)."""
    h = a["handle"]
    if not can(E, a, "mail", f"mail-with MAIL-R-5 {h}"):
        return []
    since = now_utc() - dt.timedelta(days=days)
    if h.startswith("google:"):
        return mail(E, a, since, "any", f"{{from:{email} to:{email} cc:{email}}}", cap,
                    name=f"mail-with {email} {h}")
    refs: list[str] = []
    url = (f"{GRAPH}/me/messages?$top={cap}&$search=%22participants:{up.quote(email)}%22"
           "&$select=id,subject,from,toRecipients,receivedDateTime,bodyPreview,conversationId,webLink,isRead")
    d, err = _apl(h, url)
    if err:
        E.source(f"mail-with {email} {h}", "unreadable", reason=err, fix=fix_for(h, err))
        return refs
    for m in (d or {}).get("value", []):
        ts = m.get("receivedDateTime", "")
        if parse_time(ts) and parse_time(ts) < since:
            continue
        sender = ((m.get("from") or {}).get("emailAddress") or {})
        refs.append(E.add(
            f"mail:{h}:{sid(m['id'])}", "mail", account=h, thread=m.get("conversationId"),
            sender=f"{sender.get('name','')} <{sender.get('address','')}>"[:120],
            to=", ".join(((x.get("emailAddress") or {}).get("address") or "") for x in m.get("toRecipients", []))[:300],
            subject=(m.get("subject") or "")[:200], ts=ts, snippet=(m.get("bodyPreview") or "")[:400],
            url=m.get("webLink", "")))
    E.source(f"mail-with {email} {h}", "ok", len(refs))
    return refs


# ─────────────────────────────────────────────────────────────── teams chat
def _ms_me(h: str) -> str:
    d, _ = _apl(h, f"{GRAPH}/me?$select=id")
    return (d or {}).get("id", "")


def chats(E: Evidence, a: dict, since: dt.datetime, only_with: str = "", per_chat: int = 15,
          max_chats: int = 12, include_mine: bool = False, name: str = "") -> list[str]:
    """Teams chat messages since a time. Microsoft only."""
    """teams-chat.md CHAT-1 (my chats, newest first) + CHAT-5 (messages per
    chat, top-level /chats path as the recipe insists)."""
    h = a["handle"]
    name = name or f"teams-chat CHAT-1+5 {h}"
    refs: list[str] = []
    if not h.startswith("ms:") or not can(E, a, "chat", name):
        return refs
    me_id = _ms_me(h)
    d, err = _apl(h, f"{GRAPH}/me/chats?$top=50&$expand=lastMessagePreview&$orderby=lastMessagePreview/createdDateTime%20desc")
    if err:
        d, err = _apl(h, f"{GRAPH}/me/chats?$top=50&$expand=lastMessagePreview")
    if err:
        E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
        return refs
    live = []
    for c in (d or {}).get("value", []):
        p = c.get("lastMessagePreview") or {}
        t = parse_time(p.get("createdDateTime"))
        if t and t >= since:
            live.append(c)
    live.sort(key=lambda c: (c.get("lastMessagePreview") or {}).get("createdDateTime", ""), reverse=True)
    live = live[:max_chats]

    def pull(c):
        m, e = _apl(h, f"{GRAPH}/chats/{up.quote(c['id'], safe='')}/messages?$top={per_chat}")
        mem = None
        if only_with:
            mem, _ = _apl(h, f"{GRAPH}/chats/{up.quote(c['id'], safe='')}/members")
        return c, m, e, mem

    errors = 0
    for c, m, e, mem in pmap(pull, live, 6):
        if e:
            errors += 1
            continue
        if only_with:
            emails = {(x.get("email") or "").lower() for x in (mem or {}).get("value", [])}
            if only_with.lower() not in emails:
                continue
        for msg in (m or {}).get("value", []):
            if msg.get("messageType") != "message":
                continue
            t = parse_time(msg.get("createdDateTime"))
            if not t or t < since:
                continue
            user = ((msg.get("from") or {}).get("user") or {})
            mine = user.get("id") == me_id
            if mine and not include_mine:
                continue
            refs.append(E.add(
                f"chat:{h}:{sid(c['id'])}:{msg['id']}", "chat", account=h, chat_type=c.get("chatType"),
                topic=(c.get("topic") or "")[:100], sender=user.get("displayName", ""), from_me=mine,
                ts=msg.get("createdDateTime"), text=strip_html((msg.get("body") or {}).get("content"), 500),
                url=c.get("webUrl", "")))
    E.source(name, "ok" if not errors else "partial", len(refs),
             reason=f"{errors} chat(s) could not be read" if errors else "")
    return refs


# ─────────────────────────────────────────────────────────────── github
GH_FIELDS = "number,title,repository,url,author,state,createdAt,updatedAt,closedAt,labels,assignees,commentsCount,body"


def gh_search(E: Evidence, q: str, name: str, cap: int = 30, kind: str = "prs", ident: str = "") -> list[str]:
    """github.md GH-37 (`gh search prs`) / GH-36 (`gh search issues`), as one
    identity. GH-5/6/7/16/17 are these searches with @me qualifiers, which is
    how they fan out across every repo instead of the cwd's one."""
    fields = GH_FIELDS + (",isDraft" if kind == "prs" else "")
    label = f"github {'GH-37' if kind == 'prs' else 'GH-36'} {name}" + (f" {ident}" if ident else "")
    if "@me" in q:  # the search API does not resolve @me inside a query string
        q = q.replace("@me", github_login(ident)[0] or "@me")
    args = ["search", kind, *q.split(), "--limit", str(cap), "--sort", "updated", "--json", fields]
    time.sleep(1.2)  # search API: 30 req/min and a secondary limit on bursts
    d, err = _ghx(ident, args)
    if err and "rate limit" in err.lower():
        time.sleep(30)
        d, err = _ghx(ident, args)
    if err:
        E.source(label, "unreadable", reason=err,
                 fix="wait and re-run (GitHub rate limit)" if "rate limit" in err.lower()
                 else (f"apl login {ident}" if ident else "gh auth login"))
        return []
    refs = []
    for it in d or []:
        repo = (it.get("repository") or {}).get("nameWithOwner", "")
        is_pr = kind == "prs"
        ref = f"{'pr' if is_pr else 'issue'}:{repo}#{it['number']}"
        prev = E.doc["items"].get(ref, {})
        E.add(ref, "pr" if is_pr else "issue", repo=repo, number=it["number"],
              title=it["title"][:160], state=it.get("state"), author=(it.get("author") or {}).get("login", ""),
              assignees=[x.get("login") for x in it.get("assignees", [])],
              labels=[x.get("name") for x in it.get("labels", [])], draft=it.get("isDraft", False),
              created=it.get("createdAt"), updated=it.get("updatedAt"), closed=it.get("closedAt"),
              comments=it.get("commentsCount", 0), why=sorted(set(prev.get("why", []) + [name])),
              seen_as=sorted(set(prev.get("seen_as", []) + ([ident] if ident else []))),
              url=it.get("url"), body=strip_html(it.get("body"), 600))
        refs.append(ref)
    E.source(label, "ok", len(refs))
    return refs


def gh_comments(E: Evidence, ref: str, since: dt.datetime, cap: int = 30) -> list[str]:
    """Discussion on one issue/PR since a time (decision hunting)."""
    """github.md GH-19/GH-8 (`… view --comments`), via the REST fallback so a
    time window applies."""
    it = E.doc["items"][ref]
    d, err = _gh(f"repos/{it['repo']}/issues/{it['number']}/comments", {"since": iso(since), "per_page": str(cap)},
                 ident=(it.get("seen_as") or [""])[0])
    if err:
        return []
    out = []
    for c in d or []:
        out.append(E.add(f"comment:{it['repo']}#{it['number']}:{c['id']}", "comment", repo=it["repo"],
                         number=it["number"], sender=(c.get("user") or {}).get("login", ""),
                         ts=c.get("created_at"), text=strip_html(c.get("body"), 700), url=c.get("html_url")))
    return out


def gh_login_for(email: str) -> str:
    d, _ = _gh("search/commits", {"q": f"author-email:{email}", "per_page": "1"})
    for it in (d or {}).get("items", []):
        if it.get("author"):
            return it["author"]["login"]
    d, _ = _gh("search/users", {"q": f"{email} in:email"})
    for it in (d or {}).get("items", []):
        return it["login"]
    return ""


# ─────────────────────────────────────────────────────────────── drive, notes
def doc_text(h: str, file_id: str, cap: int = 20000) -> tuple[str, str | None]:
    if h.startswith("google:"):
        out, err = _apl(h, f"{GDRIVE}/files/{file_id}/export?mimeType=text/plain", raw=True)
        if err:
            out, err = _apl(h, f"{GDRIVE}/files/{file_id}?alt=media", raw=True)
        return ((out or "")[:cap], err)
    out, err = _apl(h, f"{GRAPH}/me/drive/items/{file_id}/content?format=pdf", raw=True)
    return ((out or "")[:cap], err)


def drive_search(E: Evidence, a: dict, text: str, since: dt.datetime | None = None, cap: int = 5,
                 name: str = "") -> list[str]:
    """drive.md DRIVE-14 / docs.md fullText search (Google) and DRIVE-3 (OneDrive)."""
    h = a["handle"]
    name = name or f"drive {'DRIVE-14' if h.startswith('google:') else 'DRIVE-3'} {h}"
    refs: list[str] = []
    if not can(E, a, "drive", name):
        return refs
    words = [w for w in re.findall(r"[A-Za-z0-9][A-Za-z0-9\-]{2,}", text or "")
             if w.lower() not in {"the", "and", "with", "meeting", "call", "sync", "weekly", "daily", "for"}][:4]
    if not words:
        return refs
    if h.startswith("google:"):
        q = " and ".join(f"fullText contains '{w}'" for w in words[:3]) + " and trashed = false"
        if since:
            q += f" and modifiedTime > '{iso(since)[:19]}'"
        d, err = _apl(h, f"{GDRIVE}/files?pageSize={cap}&orderBy=modifiedTime%20desc&q={up.quote(q)}"
                         "&fields=files(id,name,mimeType,modifiedTime,webViewLink,owners(emailAddress))")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for f in (d or {}).get("files", []):
            refs.append(E.add(f"file:{h}:{f['id']}", "file", account=h, title=f["name"][:160],
                              mime=f.get("mimeType"), modified=f.get("modifiedTime"), url=f.get("webViewLink"),
                              owner=((f.get("owners") or [{}])[0]).get("emailAddress", "")))
    else:
        d, err = _apl(h, f"{GRAPH}/me/drive/root/search(q='{up.quote(' '.join(words[:3]))}')?$top={cap}"
                         "&$select=id,name,webUrl,lastModifiedDateTime,file")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for f in (d or {}).get("value", []):
            refs.append(E.add(f"file:{h}:{sid(f['id'])}", "file", account=h, title=f["name"][:160],
                              modified=f.get("lastModifiedDateTime"), url=f.get("webUrl")))
    E.source(name, "ok", len(refs))
    return refs


def meeting_notes(E: Evidence, ev_ref: str, notes_dir: str) -> list[str]:
    """Notes for one meeting: Google event attachments (Gemini notes) exported
    as text (docs.md export / DRIVE-20 with text/plain), Drive docs named after
    the meeting (DRIVE-14), Teams transcript (online-meetings.md MEET-2 resolve
    by JoinWebUrl -> MEET-6 list -> MEET-7 VTT) and the Teams meeting chat
    (MEET-4 chatInfo.threadId -> CHAT-5). Text goes to
    notes_dir/<n>.txt; the evidence item holds the path and the first lines."""
    ev = E.doc["items"][ev_ref]
    h = ev["account"]
    os.makedirs(notes_dir, exist_ok=True)
    refs: list[str] = []
    tried: list[str] = []

    def keep(ref, kind, title, text, url, via):
        path = os.path.join(notes_dir, re.sub(r"[^A-Za-z0-9_.-]", "_", ref)[-80:] + ".txt")
        with open(path, "w") as f:
            f.write(text)
        refs.append(E.add(ref, kind, account=h, meeting=ev_ref, title=title[:160], url=url, via=via,
                          path=path, chars=len(text), head=text[:300]))

    start = parse_time(ev.get("start")) or now_utc()
    if h.startswith("google:"):
        for att in ev.get("attachments", []):
            fid = att.get("file_id")
            if not fid:
                continue
            tried.append("event attachment")
            txt, err = doc_text(h, fid)
            if txt.strip():
                keep(f"notes:{h}:{fid}", "notes", att.get("title", "attachment"), txt, att.get("url", ""),
                     "calendar attachment")
        if not refs:
            tried.append("drive search")
            title = ev.get("title", "")
            q = (f"name contains '{title[:40].replace(chr(39), ' ')}' and modifiedTime > "
                 f"'{iso(start - dt.timedelta(hours=2))[:19]}' and trashed = false")
            d, err = _apl(h, f"{GDRIVE}/files?pageSize=5&q={up.quote(q)}&fields=files(id,name,mimeType,webViewLink)")
            for f in (d or {}).get("files", []):
                txt, err = doc_text(h, f["id"])
                if txt.strip():
                    keep(f"notes:{h}:{f['id']}", "notes", f["name"], txt, f.get("webViewLink", ""), "drive")
    else:
        join = ev.get("join_url", "")
        if join:
            tried.append("teams transcript MEET-2/6/7")
            flt = up.quote(f"JoinWebUrl eq '{join}'")
            d, err = _apl(h, f"{GRAPH}/me/onlineMeetings?$filter={flt}")
            m = ((d or {}).get("value") or [None])[0]
            if m:
                tr, err = _apl(h, f"{GRAPH}/me/onlineMeetings/{m['id']}/transcripts")
                for t in (tr or {}).get("value", []):
                    vtt, e2 = _apl(h, f"{GRAPH}/me/onlineMeetings/{m['id']}/transcripts/{t['id']}/content?$format=text/vtt",
                                   raw=True)
                    if vtt and vtt.strip():
                        keep(f"transcript:{h}:{sid(t['id'])}", "transcript", ev.get("title", "") + " transcript",
                             vtt[:60000], "", "teams transcript")
                if err and not refs:
                    E.source(f"transcript {ev.get('title','')[:40]}", "unreadable", reason=err,
                             fix=f"meeting organiser must enable transcription, or {fix_for(h, err)}")
                thread = (m.get("chatInfo") or {}).get("threadId")
                if thread:
                    tried.append("teams meeting chat MEET-4+CHAT-5")
                    cm, e3 = _apl(h, f"{GRAPH}/chats/{up.quote(thread, safe='')}/messages?$top=50")
                    lines = []
                    for msg in reversed((cm or {}).get("value", [])):
                        if msg.get("messageType") != "message":
                            continue
                        t = parse_time(msg.get("createdDateTime"))
                        if t and t < start - dt.timedelta(hours=1):
                            continue
                        who = (((msg.get("from") or {}).get("user") or {}).get("displayName") or "?")
                        txt = strip_html((msg.get("body") or {}).get("content"), 2000)
                        if txt:
                            lines.append(f"[{msg.get('createdDateTime','')[:16]}] {who}: {txt}")
                    if lines:
                        keep(f"meetchat:{h}:{sid(thread)}", "meeting-chat", ev.get("title", "") + " meeting chat",
                             "\n".join(lines), "", "teams meeting chat")
            elif err:
                E.source(f"online-meeting {ev.get('title','')[:40]}", "unreadable", reason=err, fix=fix_for(h, err))
    ev["notes_tried"] = tried
    ev["notes_found"] = len(refs)
    return refs


# ─────────────────────────────────────────────────────────────── tasks
def tasks(E: Evidence, a: dict, due_before: dt.datetime, name: str = "") -> list[str]:
    """tasks.md: Google Tasks lists + open tasks; Microsoft To Do lists + tasks
    with status ne 'completed'. Only what is due by `due_before`."""
    h = a["handle"]
    name = name or f"tasks {h}"
    refs: list[str] = []
    if not can(E, a, "tasks", name):
        return refs
    if h.startswith("google:"):
        ls, err = _apl(h, f"{GTASKS}/users/@me/lists?maxResults=20")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for l in (ls or {}).get("items", []):
            d, err = _apl(h, f"{GTASKS}/lists/{l['id']}/tasks?showCompleted=false&maxResults=50"
                             f"&dueMax={iso(due_before)}")
            for t in (d or {}).get("items", []):
                refs.append(E.add(f"task:{h}:{t['id']}", "task", account=h, list=l.get("title"),
                                  title=(t.get("title") or "")[:160], due=t.get("due"),
                                  notes=(t.get("notes") or "")[:300], url=t.get("webViewLink", "")))
    else:
        ls, err = _apl(h, f"{GRAPH}/me/todo/lists")
        if err:
            E.source(name, "unreadable", reason=err, fix=fix_for(h, err))
            return refs
        for l in (ls or {}).get("value", []):
            d, err = _apl(h, f"{GRAPH}/me/todo/lists/{l['id']}/tasks?$filter=status%20ne%20'completed'&$top=50")
            for t in (d or {}).get("value", []):
                due = parse_time(((t.get("dueDateTime") or {}).get("dateTime")))
                if not due or due > due_before:
                    continue
                refs.append(E.add(f"task:{h}:{sid(t['id'])}", "task", account=h, list=l.get("displayName"),
                                  title=(t.get("title") or "")[:160], due=iso(due)))
    E.source(name, "ok", len(refs))
    return refs


# ─────────────────────────────────────────────────────────────── people
def person(E: Evidence, accts: list[dict], email: str) -> str:
    """Name + organisation for one address: contacts.md CONT-5 (searchContacts),
    CONT-6 (otherContacts), the MAIL-W-0 resolution order, and Graph People
    (People.Read) for Microsoft."""
    email = email.lower()
    info = {"email": email, "domain": domain_of(email)}
    for a in accts:
        h = a["handle"]
        if h.startswith("google:"):
            for ep in ("people:searchContacts", "otherContacts:search"):
                mask = "names,emailAddresses,organizations" if ep.startswith("people") else "names,emailAddresses"
                d, err = _apl(h, f"{GPEOPLE}/{ep}?query={up.quote(email)}&readMask={mask}")
                for r in (d or {}).get("results", []):
                    p = r.get("person", {})
                    if (p.get("names") or []) and not info.get("name"):
                        info["name"] = p["names"][0].get("displayName")
                    orgs = p.get("organizations") or []
                    if orgs and not info.get("org"):
                        info["org"] = orgs[0].get("name")
                        info["role"] = orgs[0].get("title")
                    info.setdefault("via", h)
        else:
            d, err = _apl(h, f"{GRAPH}/me/people?$search=%22{up.quote(email)}%22&$top=1"
                             "&$select=displayName,companyName,jobTitle,department,scoredEmailAddresses")
            for p in (d or {}).get("value", []):
                info.setdefault("name", p.get("displayName"))
                if p.get("companyName"):
                    info.setdefault("org", p.get("companyName"))
                if p.get("jobTitle"):
                    info.setdefault("role", p.get("jobTitle"))
                info.setdefault("via", h)
    return E.add(f"person:{email}", "person", **info)


# ─────────────────────────────────────────────────────────────── state
def wfx_state(scope: str, key: str) -> str:
    try:
        p = subprocess.run(["wfx", "state", "get", f"--{scope}", key], capture_output=True, text=True, timeout=30)
        return p.stdout.strip() if p.returncode == 0 else ""
    except Exception:
        return ""


def wfx_state_set(scope: str, key: str, value: str) -> bool:
    try:
        p = subprocess.run(["wfx", "state", "set", f"--{scope}", key, value], capture_output=True, text=True,
                           timeout=30)
        return p.returncode == 0
    except Exception:
        return False


def meeting_class(ev: dict, internal: set[str], me: set[str]) -> dict:
    """Who is in the room: external orgs, size, whether it is a 1:1."""
    others = [x for x in ev.get("attendees", []) if x.get("email") and x["email"] not in me
              and not x.get("self") and "resource.calendar" not in x["email"]]
    ext = sorted({domain_of(x["email"]) for x in others if not is_internal(x["email"], internal)})
    title = ev.get("title", "")
    one = len(others) == 1 or bool(re.search(r"\b(1:1|1-1|1on1|one[- ]on[- ]one)\b", title, re.I))
    return {"others": len(others), "external_domains": ext, "external": bool(ext), "one_on_one": one}


if __name__ == "__main__":
    print("sources.py is a library; each template's collect.py drives it.", file=sys.stderr)
    sys.exit(2)
