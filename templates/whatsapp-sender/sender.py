#!/usr/bin/env python3
"""whatsapp-sender: one send, applied through the subscription's responsePolicy.

    sender.py plan      reads input.json, writes plan.json, exits with the decision
    sender.py deliver   reads plan.json, acts on it, prints the typed result

plan exit codes (the workflow routes on them):
    0  auto      send now
    10 approval  a person approves first
    11 draft     store the draft, send nothing
    12 none      skip
    4  already   this key was handled before (sent or uncertain) — never resent
    2  bad input

deliver prints ONE JSON object:
    {"status": "sent|failed|uncertain|drafted|skipped|already",
     "id": "<transport id>", "action": "...", "chat": "...", "key": "...", "error": "..."}
and exits 0 (sent/drafted/skipped/already), 5 (failed) or 6 (uncertain).

The ledger (JSON lines, one row per state change) is what makes "never resend
an uncertain send" true: a `pending` row is written BEFORE the adapter runs, so
a crash in the middle leaves the key pending, and pending/uncertain/sent keys
are never sent again. Only `failed` may be retried — the transport said no.
"""
import hashlib
import json
import os
import shlex
import subprocess
import sys
from datetime import datetime, timezone

POLICIES = {"auto": 0, "approval": 10, "draft": 11, "none": 12}
NO_RESEND = {"pending", "uncertain", "sent"}


def now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def ledger_path():
    p = os.environ.get("WA_SENDER_LEDGER", "").strip()
    if p:
        return p
    base = os.environ.get("WFX_CHANNEL_STATE_DIR") or os.path.expanduser("~/.local/state/wfnexus")
    return os.path.join(base, "whatsapp-sender.ledger.jsonl")


def ledger_last(key):
    last = None
    try:
        with open(ledger_path()) as f:
            for line in f:
                try:
                    row = json.loads(line)
                except ValueError:
                    continue
                if row.get("key") == key:
                    last = row
    except FileNotFoundError:
        pass
    return last


def ledger_write(row):
    path = ledger_path()
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    row = dict(row, at=now())
    with open(path, "a") as f:
        f.write(json.dumps(row) + "\n")
        f.flush()
        os.fsync(f.fileno())


def action_of(i):
    if i.get("delete"):
        return "delete"
    if i.get("edit"):
        return "edit"
    if i.get("reactTo"):
        return "reaction"
    if i.get("media"):
        return "media"
    if i.get("replyTo"):
        return "reply"
    return "text"


def plan():
    i = json.load(open("input.json"))
    chat = (i.get("chat") or "").strip()
    policy = (i.get("responsePolicy") or "auto").strip()
    act = action_of(i)
    problems = []
    if not chat:
        problems.append("chat is required")
    if policy not in POLICIES:
        problems.append("responsePolicy must be one of draft, approval, auto, none")
    if act in ("text", "reply", "edit") and not (i.get("text") or "").strip():
        problems.append(act + " needs text")
    if act == "reaction" and "reaction" not in i:
        problems.append("reaction needs an emoji (empty removes it)")
    if problems:
        print("invalid input: " + "; ".join(problems))
        sys.exit(2)
    key = (i.get("key") or "").strip()
    if not key:
        # Same request twice => same key => sent once.
        basis = json.dumps({k: i.get(k) for k in sorted(i) if k not in ("responsePolicy",)}, sort_keys=True)
        key = "wa-" + hashlib.sha256(basis.encode()).hexdigest()[:24]
    p = {"key": key, "chat": chat, "action": act, "policy": policy, "input": i}
    prev = ledger_last(key)
    if prev and prev.get("status") in NO_RESEND:
        p["already"] = prev
    json.dump(p, open("plan.json", "w"))
    print(f"{act} to {chat} — policy {policy}, key {key}" + (f" (already {prev['status']})" if "already" in p else ""))
    if "already" in p:
        sys.exit(4)
    sys.exit(POLICIES[policy])


def envelope(p):
    i = p["input"]
    env = {
        "version": "wfnexus.channel/v1", "type": "send",
        "channel": os.environ.get("WA_CHANNEL", ""), "target": p["chat"],
        "text": i.get("text") or "", "dedupKey": p["key"], "at": now(),
    }
    if i.get("mentions"):
        env["mentions"] = [m.strip() for m in str(i["mentions"]).split(",") if m.strip()]
    if p["action"] == "reply" or (i.get("replyTo") and p["action"] in ("text", "media")):
        env["replyTo"] = {"id": i["replyTo"], "sender": i.get("replyToSender") or ""}
    if p["action"] == "media":
        env["attachment"] = {"path": i["media"], "kind": i.get("mediaKind") or "file",
                             "filename": os.path.basename(i["media"])}
    if p["action"] == "reaction":
        env["reaction"] = {"targetId": i["reactTo"], "emoji": i.get("reaction") or "",
                           "sender": i.get("replyToSender") or ""}
    if p["action"] == "edit":
        env["edit"] = {"id": i["edit"]}
    if p["action"] == "delete":
        env["delete"] = {"id": i["delete"]}
    return env


def finish(res):
    print(json.dumps(res))
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        verdict = {"sent": "ok", "drafted": "ok", "skipped": "ok", "already": "ok"}.get(res["status"], "failed")
        f.write(f"{verdict}: {res['status']} {res['action']} to {res['chat']}")
    sys.exit({"failed": 5, "uncertain": 6}.get(res["status"], 0))


def deliver():
    p = json.load(open("plan.json"))
    res = {"status": "", "id": "", "action": p["action"], "chat": p["chat"], "key": p["key"], "error": ""}
    if "already" in p:
        prev = p["already"]
        res.update(status="already", id=prev.get("id", ""),
                   error=f"previous attempt is {prev.get('status')}; not resent")
        finish(res)
    if p["policy"] == "none":
        res["status"] = "skipped"
        finish(res)
    if p["policy"] == "draft":
        ledger_write({"key": p["key"], "status": "drafted", "chat": p["chat"], "action": p["action"],
                      "text": p["input"].get("text") or ""})
        res["status"] = "drafted"
        finish(res)
    # auto, or approval that a person already approved (the run paused before this step).
    cmd = os.environ.get("WA_ADAPTER", "").strip()
    if not cmd:
        res.update(status="failed", error="WA_ADAPTER is not set — nothing to send with")
        finish(res)
    ledger_write({"key": p["key"], "status": "pending", "chat": p["chat"], "action": p["action"]})
    timeout = int(os.environ.get("WA_TIMEOUT_SEC", "60"))
    try:
        r = subprocess.run(shlex.split(cmd) + ["send"], input=json.dumps(envelope(p)),
                           capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        res.update(status="uncertain", error=f"adapter did not answer in {timeout}s; it may have gone out")
        ledger_write({"key": p["key"], "status": "uncertain", "chat": p["chat"], "error": res["error"]})
        finish(res)
    out = {}
    try:
        out = json.loads(r.stdout.strip() or "{}")
    except ValueError:
        pass
    if r.returncode == 0 and out.get("status", "sent") == "sent":
        res.update(status="sent", id=out.get("id", ""))
    elif out.get("status") == "uncertain" or r.returncode == 0:
        res.update(status="uncertain", id=out.get("id", ""),
                   error=out.get("error") or r.stderr.strip()[-300:] or "adapter could not confirm the send")
    else:
        res.update(status="failed", error=out.get("error") or r.stderr.strip()[-300:] or f"adapter exit {r.returncode}")
    ledger_write({"key": p["key"], "status": res["status"], "id": res["id"], "chat": p["chat"], "error": res["error"]})
    finish(res)


if __name__ == "__main__":
    {"plan": plan, "deliver": deliver}[sys.argv[1]]()
