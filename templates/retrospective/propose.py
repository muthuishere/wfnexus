#!/usr/bin/env python3
"""retrospective: put each checked proposal in front of a person.

    propose.py   reads checked.json (written by check.py); for EACH
                 proposal starts one apply-mode run of this workflow, which
                 stops at its approval gate — one pending item per proposal in
                 `wfx approvals` / the approval desk. Exit 0, or 2 when a run
                 cannot be started (a proposal is never silently lost).

Approve → that run's apply step makes the change (apply.py). Reject → the run
is cancelled; the next retrospective remembers the fingerprint and never
proposes it again.
"""
import json
import os
import sys
import urllib.parse
import urllib.request

SELF = os.environ.get("RETRO_WORKFLOW", "retrospective")


def api(path, method="GET", body=None):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + "/api" + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read() or b"null")


def main():
    try:
        checked = json.load(open("checked.json"))
    except Exception as e:
        print(f"the checked result is unreadable: {e!r}")
        return 2
    started, failed = [], []
    for p in checked.get("proposals") or []:
        try:
            r = api(f"/workflows/{urllib.parse.quote(SELF, safe='')}/runs", "POST",
                    {"mode": "apply", "proposal": p, "fingerprint": p["fingerprint"]})
            started.append({"fingerprint": p["fingerprint"], "kind": p["kind"], "target": p["target"],
                            "runId": ((r or {}).get("run") or r or {}).get("id")})
        except Exception as e:
            failed.append({"fingerprint": p.get("fingerprint"), "error": str(e)[:300]})
    print(json.dumps({"pending": started, "failed": failed}, indent=2))
    return 2 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
