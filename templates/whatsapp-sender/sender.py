#!/usr/bin/env python3
"""whatsapp-sender: one send, applied through the caller's responsePolicy (missing = draft: never auto-send).

    sender.py plan      reads input.json, writes plan.json, prints the planned op as JSON
    sender.py policy    exits 10 when a person approves first
    sender.py deliver   reads plan.json + sent.json, prints the typed result

The SEND itself is the workflow's `send:` step (issue #86): every op rides the
channel contract, so the channel's sendGap paces it, the adapter's echo ledger
records it, and the server's dedup claims the key before the adapter runs —
a sent or unconfirmed key is never sent again; a refused one may be retried.

plan exit codes (the workflow routes on them):
    0  send (auto, or approval after a person says yes)
    11 draft     nothing is sent
    12 none      skip
    2  bad input

deliver prints ONE JSON object:
    {"status": "sent|drafted|skipped|already", "id": "...", "action": "...", "chat": "...", "key": "..."}
A failed or unconfirmed send fails the run at the send step instead.
"""
import hashlib
import json
import os
import sys

POLICIES = {"auto": 0, "approval": 0, "draft": 11, "none": 12}


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
    policy = (i.get("responsePolicy") or "draft").strip()
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
    if act == "media" and (i.get("mediaKind") or "file") not in ("file", "image", "voice", "sticker"):
        problems.append("mediaKind must be file, image, voice or sticker")
    if problems:
        print("invalid input: " + "; ".join(problems))
        sys.exit(2)
    key = (i.get("key") or "").strip()
    if not key:
        # Same request twice => same key => sent once.
        basis = json.dumps({k: i.get(k) for k in sorted(i) if k not in ("responsePolicy",)}, sort_keys=True)
        key = "wa-" + hashlib.sha256(basis.encode()).hexdigest()[:24]
    # Only the chosen op's fields: the send step renders absent ones empty.
    p = {"key": key, "chat": chat, "action": act, "policy": policy, "text": i.get("text") or ""}
    if i.get("mentions") and act not in ("reaction", "delete"):
        p["mentions"] = str(i["mentions"])
    if act in ("reply", "media") and i.get("replyTo"):
        p["replyTo"], p["replyToSender"] = i["replyTo"], i.get("replyToSender") or ""
    if act == "media":
        p["media"], p["mediaKind"] = i["media"], i.get("mediaKind") or "file"
    if act == "reaction":
        p["reactTo"], p["reaction"] = i["reactTo"], i.get("reaction") or ""
        p["replyToSender"] = i.get("replyToSender") or ""
    if act == "edit":
        p["edit"] = i["edit"]
    if act == "delete":
        p["delete"], p["text"] = i["delete"], ""
    json.dump(p, open("plan.json", "w"))
    print(json.dumps(p))
    sys.exit(POLICIES[policy])


def policy():
    p = json.load(open("plan.json"))
    print(f"{p['action']} to {p['chat']} — policy {p['policy']}, key {p['key']}")
    sys.exit(10 if p["policy"] == "approval" else 0)


def deliver():
    p = json.load(open("plan.json"))
    res = {"status": "", "id": "", "action": p["action"], "chat": p["chat"], "key": p["key"]}
    try:
        sent = json.load(open("sent.json")) or {}
    except (ValueError, FileNotFoundError):
        sent = {}
    if not isinstance(sent, dict):
        sent = {}
    if p["policy"] == "none":
        res["status"] = "skipped"
    elif p["policy"] == "draft":
        res.update(status="drafted", text=p.get("text", ""))
    elif sent.get("sent"):
        res.update(status="sent", id=sent.get("id", ""))
    elif sent.get("skipped") == "dedup":
        res.update(status="already", error="this key was already sent (or may have been); not resent")
    else:
        res.update(status="skipped", error="the send step did not run")
    print(json.dumps(res))
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(f"ok: {res['status']} {res['action']} to {res['chat']}")


if __name__ == "__main__":
    {"plan": plan, "policy": policy, "deliver": deliver}[sys.argv[1]]()
