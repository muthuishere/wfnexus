#!/usr/bin/env python3
"""whatsapp-subscription-manager: add/update/pause/resume/rm/ls/validate through $WFX_API.

    subs.py plan    reads input.json, checks it against the live list, writes plan.json
                    exit 0 = go, 10 = a person approves first, 2 = invalid (reason on stdout)
    subs.py apply   does it, then READS IT BACK and prints the typed result:
                    {"action", "name", "ok", "verified", "status", "problem", "warning",
                     "subscription", "subscriptions", "error"}
                    exit 0, or 5 when the API refused or the read-back disagrees

Needs a person (exit 10): rm, and any add/update that would let EVERYONE
(`allow: *`) start an auto-replying workflow.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

FIELDS = ["channel", "chat", "events", "interval", "workflow", "version", "skills", "instructions",
          "responsePolicy", "memory", "mode", "activation", "allow", "owner"]
LISTS = {"events", "skills", "allow"}
ENUMS = {"interval": {"30s", "1m", "2m"}, "responsePolicy": {"draft", "approval", "auto", "none"},
         "mode": {"fanout", "exclusive", "observe"}, "activation": {"mention", "always"}}
ACTIONS = {"add", "update", "pause", "resume", "rm", "ls", "validate"}


def api(method, path, body=None):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    req = urllib.request.Request(base + "/api" + path, method=method,
                                 data=None if body is None else json.dumps(body).encode())
    req.add_header("Content-Type", "application/json")
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, {"error": raw.decode(errors="replace")[:300]}


def as_list(v):
    if v is None or v == "":
        return None
    if isinstance(v, list):
        return [str(x).strip() for x in v if str(x).strip()]
    return [x.strip() for x in str(v).split(",") if x.strip()]


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


def die(msg):
    print("invalid: " + msg)
    outcome("failed: " + msg)
    sys.exit(2)


def plan():
    i = json.load(open("input.json"))
    act = (i.get("action") or "").strip()
    name = (i.get("name") or "").strip()
    if act not in ACTIONS:
        die(f"action must be one of {', '.join(sorted(ACTIONS))}")
    if act != "ls" and not name:
        die("name is required")
    fields = {}
    for k in FIELDS:
        v = i.get(k)
        if k in LISTS:
            v = as_list(v)
        if v not in (None, ""):
            if k in ENUMS and v not in ENUMS[k]:
                die(f"{k} must be one of {', '.join(sorted(ENUMS[k]))}")
            fields[k] = v
    existing = None
    if act != "ls":
        code, body = api("GET", "/subscriptions/" + urllib.parse.quote(name))
        if code == 200:
            existing = body.get("subscription")
        elif code != 404:
            die(f"could not read subscription {name}: HTTP {code} {body}")
    if act == "add":
        if existing and existing.get("status") != "deleted":
            die(f"{name} already exists — use update")
        missing = [k for k in ("channel", "chat", "workflow") if k not in fields]
        if missing:
            die("add needs " + ", ".join(missing))
        if not fields.get("allow"):
            die("add needs `allow`: who may start a run (an empty list means nobody — say `*` for everyone)")
        sub = fields
    elif act == "update":
        if not existing:
            die(f"no subscription {name}")
        sub = {k: existing.get(k) for k in FIELDS if existing.get(k) not in (None, "", [])}
        sub.update(fields)
    elif act == "ls":
        sub = fields  # `channel` narrows the list
    else:
        if not existing:
            die(f"no subscription {name}")
        sub = existing
    p = {"action": act, "name": name, "sub": sub, "before": existing}
    json.dump(p, open("plan.json", "w"))
    risky = act == "rm" or (act in ("add", "update") and "*" in (sub.get("allow") or [])
                             and (sub.get("responsePolicy") or "auto") == "auto")
    print(f"{act} {name or '(all)'}" + (" — needs approval" if risky else ""))
    if risky:
        print(json.dumps(sub, indent=2))
    sys.exit(10 if risky else 0)


def apply():
    p = json.load(open("plan.json"))
    act, name, sub = p["action"], p["name"], p["sub"]
    q = urllib.parse.quote(name)
    res = {"action": act, "name": name, "ok": False, "verified": False, "status": "", "problem": "",
           "warning": "", "subscription": None, "subscriptions": [], "error": ""}
    if act == "ls":
        code, body = api("GET", "/subscriptions")
        if code != 200:
            res["error"] = f"HTTP {code} {body}"
        else:
            ch = (sub or {}).get("channel") if sub else None
            res["subscriptions"] = [s for s in body if not ch or s.get("channel") == ch]
            res["ok"] = res["verified"] = True
    else:
        if act in ("add", "update"):
            code, body = api("PUT", "/subscriptions/" + q, sub)
        elif act == "rm":
            code, body = api("DELETE", "/subscriptions/" + q)
        else:
            code, body = api("POST", f"/subscriptions/{q}/{act}")
        if code != 200:
            res["error"] = f"the API refused {act}: HTTP {code} {(body or {}).get('error', body)}"
        else:
            res["ok"] = True
            # Read it back: the answer to "did it happen" is the server's, not ours.
            code, back = api("GET", "/subscriptions/" + q)
            s = (back or {}).get("subscription") if code == 200 else None
            res["subscription"] = s
            if s:
                res.update(status=s.get("status", ""), problem=s.get("problem", ""), warning=s.get("warning", ""))
            want = {"pause": {"paused"}, "resume": {"active", "validating"}, "rm": {"deleted", ""},
                    "add": {"active", "validating"}, "update": {"active", "validating", "paused"},
                    "validate": {"active", "validating"}}[act]
            res["verified"] = (s is None and act == "rm") or (s is not None and s.get("status", "") in want)
            if not res["verified"]:
                res["error"] = (s or {}).get("error") or f"read-back status {res['status'] or 'missing'} is not {'/'.join(sorted(want))}"
    print(json.dumps(res))
    good = res["ok"] and res["verified"]
    detail = f" ({res['warning']})" if res["warning"] else ""
    outcome((f"ok: {act} {name} → {res['status'] or 'done'}{detail}" if good
             else f"failed: {act} {name}: {res['error']}") if act != "ls"
            else f"ok: {len(res['subscriptions'])} subscriptions")
    sys.exit(0 if good else 5)


if __name__ == "__main__":
    {"plan": plan, "apply": apply}[sys.argv[1]]()
