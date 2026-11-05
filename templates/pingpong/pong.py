#!/usr/bin/env python3
"""pingpong: the reply is "pong #N", where N counts this conversation's pongs.

    pong.py reply   reads input.json + memory.md, prints the reply (no newline);
                    exits 3 when the event is not a text message (nothing to answer)
    pong.py save    reads input.json + sent.json, writes the run's outcome line —
                    which is what the NEXT run of this conversation reads back
"""
import json
import os
import re
import sys


def message():
    i = json.load(open("input.json"))
    m = i.get("message") or {}
    if not m and i.get("messages"):  # a batch of messages within 3 s: answer the last
        m = i["messages"][-1]
    return m


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


def reply():
    m = message()
    if (m.get("kind") or "message") != "message" or not (m.get("text") or "").strip():
        print("not a text message — nothing to answer")
        outcome("ok: not a text message, nothing to answer")
        sys.exit(3)
    memory = open("memory.md").read() if os.path.exists("memory.md") else ""
    seen = [int(n) for n in re.findall(r"pong #(\d+)", memory)]
    said = f"pong #{(max(seen) if seen else 0) + 1}"
    open("reply.txt", "w").write(said)
    print(said, end="")


def save():
    m = message()
    said = open("reply.txt").read().strip()
    res = json.loads(open("sent.json").read() or "{}")
    status = res.get("status", "unknown")
    outcome(f"ok: {said} — answered {m.get('from', '?')} in {m.get('chat', '?')}, {status} {res.get('id', '')}".strip())
    print(json.dumps({"heard": (m.get("text") or "")[:200], "said": said, "status": status, "id": res.get("id", "")}))


if __name__ == "__main__":
    {"reply": reply, "save": save}[sys.argv[1]]()
