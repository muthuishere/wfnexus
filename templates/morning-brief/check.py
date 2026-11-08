#!/usr/bin/env python3
"""morning-brief: keep only brief lines that cite real facts. No invented items.

    check.py   reads facts.json + brief.json; prints {text, lines, dropped, changed};
               exit 0, or 2 when the brief is malformed (the run fails, nothing is sent)

A line is kept only when it has text and cites at least one id, and EVERY id it
cites is a fact id in facts.json. Anything else is DROPPED and listed. At most
MAX_LINES (12) are kept. `changed` compares the cited ids with yesterday's.
"""
import json
import os
import sys

MAX_LINES = int(os.environ.get("MAX_LINES", "12"))


def main():
    try:
        facts = json.load(open("facts.json"))
        raw = json.load(open("brief.json"))["lines"]
        if not isinstance(raw, list):
            raise ValueError("lines is not a list")
    except Exception as e:
        print(f"the brief is malformed: {e!r}")
        return 2
    ids = {f.get("id") for f in facts.get("facts") or []}
    keep, dropped = [], []
    for ln in raw:
        ok = isinstance(ln, dict)
        text = " ".join(str(ln.get("text") or "").split()) if ok else ""
        cites = [str(c) for c in (ln.get("cites") or [])] if ok else []
        if not text:
            why = "no text"
        elif not cites:
            why = "cites nothing"
        elif any(c not in ids for c in cites):
            why = f"cites {[c for c in cites if c not in ids]}, not in the facts"
        elif len(keep) >= MAX_LINES:
            why = f"over {MAX_LINES} lines"
        else:
            keep.append({"text": text, "cites": cites})
            continue
        dropped.append({"line": ln, "why": why})
    before = {c for p in facts.get("previous") or [] for c in (p.get("cites") or [])}
    now = {c for k in keep for c in k["cites"]}
    body = "\n".join(f"- {k['text']} [{', '.join(k['cites'])}]" for k in keep) or "- Nothing needs you this morning."
    print(json.dumps({"text": "Morning brief\n" + body, "lines": keep, "dropped": dropped,
                      "changed": {"new": sorted(now - before), "gone": sorted(before - now)}},
                     separators=(",", ":")))
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(f"ok: {len(keep)} lines, {len(dropped)} dropped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
