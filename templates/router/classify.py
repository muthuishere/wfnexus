#!/usr/bin/env python3
"""router: pick ONE workflow for an event from routes.json; print its name.

routes.json is a list, first match wins:

    [{"name": "...", "when": {"kind": "message", "chatType": "group",
                              "text": "(?i)^/deploy", "chat": "...", "source": "...",
                              "fromOwner": true},
      "workflow": "deploy-request"},
     {"name": "fallback", "when": {}, "workflow": "frontdesk"}]

Every key under `when` must match (AND); `text` is a regex, the rest equal.
Exit 0 = routed (stdout is the workflow name, no newline), 3 = no route,
2 = routes.json is broken. The event text is untrusted: it is only ever
matched, never executed or templated into a command.
"""
import json
import os
import re
import sys


def outcome(line):
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write(line)


def field(ev, key):
    if key == "text":
        return ev.get("text") or ""
    if key == "kind":
        return ev.get("kind") or "message"
    return ev.get(key)


def matches(ev, when):
    for k, want in (when or {}).items():
        got = field(ev, k)
        if k == "text":
            if not re.search(want, got or ""):
                return False
        elif got != want:
            return False
    return True


def main():
    ev = json.load(open("input.json")).get("event") or {}
    try:
        routes = json.load(open(os.environ.get("ROUTES_FILE", "routes.json")))
        assert isinstance(routes, list)
        for r in routes:
            assert r.get("workflow"), f"route {r.get('name')} names no workflow"
            if "text" in (r.get("when") or {}):
                re.compile(r["when"]["text"])
    except Exception as e:  # a broken table is an error, not "no route"
        print(f"routes.json is broken: {e}")
        sys.exit(2)
    for r in routes:
        if matches(ev, r.get("when")):
            outcome(f"ok: routed to {r['workflow']} by {r.get('name', 'unnamed rule')}")
            print(r["workflow"], end="")
            return
    outcome(f"ok: no route for a {field(ev, 'kind')} event")
    sys.stderr.write("no route matched\n")
    sys.exit(3)


if __name__ == "__main__":
    main()
