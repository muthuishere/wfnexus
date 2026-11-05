#!/usr/bin/env python3
"""whatsapp-monitor: is the WhatsApp channel actually HEARING, not just connected?

    check.py          reads the channel, its messages and subscriptions from $WFX_API
                      (and, if STORE_PROBE is set, the transport's own store), writes
                      report.json and alert.txt; exit 0 healthy, 3 findings, 2 the check broke
    check.py key      prints the alert's dedup key: same findings in the same
                      ALERT_EVERY_H window => same key => the sender sends it once

Findings (report.json `findings[]`, each {kind, severity, subject, detail}):
    deaf             the store has newer messages than wfnexus by more than LAG_MAX_SEC
                     (OpenClaw's most-reported failure: connected but deaf)
    receive-stale    the last successful receive is older than RECEIVE_MAX_SEC
    receive-error    the last receive failed
    subscription     a subscription on this channel is in error, or cannot route (problem)
    unknown-self     the channel's own ids are unknown, so group mentions start nothing
    validating       a subscription has been validating for longer than one check
"""
import hashlib
import json
import os
import shlex
import subprocess
import sys
import urllib.parse
import urllib.request
from datetime import datetime, timezone


def api(path):
    base = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    req = urllib.request.Request(base + "/api" + path)
    tok = os.environ.get("WFX_TOKEN", "")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read() or b"null")


def ts(v):
    if not v:
        return None
    try:
        return datetime.fromisoformat(str(v).replace("Z", "+00:00"))
    except ValueError:
        return None


def iso(d):
    return d.strftime("%Y-%m-%dT%H:%M:%SZ") if d else ""


def store_latest():
    """STORE_PROBE prints {"latest": "<iso time>"} for the newest message the
    transport's store holds, e.g. a read-only `apl … messages --limit 1` wrapper."""
    cmd = os.environ.get("STORE_PROBE", "").strip()
    if not cmd:
        return None, "STORE_PROBE not set — lag not measured"
    r = subprocess.run(shlex.split(cmd), capture_output=True, text=True, timeout=60)
    if r.returncode != 0:
        raise RuntimeError(f"STORE_PROBE failed (exit {r.returncode}): {r.stderr.strip()[-200:]}")
    return ts(json.loads(r.stdout).get("latest")), ""


def check():
    ch_name = os.environ.get("WA_CHANNEL", "whatsapp")
    lag_max = int(os.environ.get("LAG_MAX_SEC", "600"))
    recv_max = int(os.environ.get("RECEIVE_MAX_SEC", "600"))
    now = datetime.now(timezone.utc)
    findings = []

    def add(kind, severity, subject, detail):
        findings.append({"kind": kind, "severity": severity, "subject": subject, "detail": detail})

    chans = api("/channels").get("channels") or []
    ch = next((c for c in chans if c.get("name") == ch_name), None)
    if ch is None:
        raise RuntimeError(f"channel {ch_name} is not configured on this server")
    last_recv = ts(ch.get("lastReceive"))
    if ch.get("lastError"):
        add("receive-error", "error", ch_name, ch["lastError"][:300])
    if last_recv is None or (now - last_recv).total_seconds() > recv_max:
        add("receive-stale", "error", ch_name,
            f"last successful receive {iso(last_recv) or 'never'}; expected within {recv_max}s")

    msgs = api("/channels/messages?" + urllib.parse.urlencode({"channel": ch_name, "limit": 1})) or []
    last_event = max((ts(m.get("at")) or ts(m.get("receivedAt")) for m in msgs), default=None) if msgs else None
    store, note = store_latest()
    lag = None
    if store is not None:
        lag = int((store - last_event).total_seconds()) if last_event else int((now - store).total_seconds())
        if lag > lag_max:
            add("deaf", "critical", ch_name,
                f"the WhatsApp store has a message from {iso(store)} but wfnexus last saw one at "
                f"{iso(last_event) or 'never'} ({lag}s behind; limit {lag_max}s) — connected but not hearing")

    subs = [s for s in (api("/subscriptions") or []) if s.get("channel") == ch_name]
    for s in subs:
        st = s.get("status", "")
        if st == "error":
            add("subscription", "error", s["name"], s.get("error") or "status error")
        elif s.get("problem") and st not in ("paused", "deleted"):
            add("subscription", "error", s["name"], s["problem"])
        if st == "validating":
            add("validating", "warning", s["name"], "still validating")
        w = s.get("warning") or ""
        if "self" in w and st == "active":
            add("unknown-self", "warning", s["name"], w)

    report = {"healthy": not findings, "channel": ch_name, "checkedAt": iso(now),
              "lastReceive": iso(last_recv), "lastEvent": iso(last_event), "storeLatest": iso(store),
              "lagSec": lag, "note": note, "subscriptions": len(subs), "findings": findings}
    json.dump(report, open("report.json", "w"), indent=2)
    with open(os.environ.get("WFX_OUTCOME", os.devnull), "w") as f:
        f.write("ok: WhatsApp healthy" if not findings
                else f"failed: WhatsApp {', '.join(sorted({x['kind'] for x in findings}))}")
    if not findings:
        print(f"healthy: {len(subs)} subscriptions, last event {iso(last_event) or 'none'}, lag {lag if lag is not None else 'n/a'}s")
        return 0
    lines = [f"wfnexus: WhatsApp channel {ch_name} needs attention"]
    for x in findings:
        lines.append(f"- [{x['severity']}] {x['kind']} {x['subject']}: {x['detail']}")
    open("alert.txt", "w").write("\n".join(lines))
    print("\n".join(lines), end="")
    return 3


def key():
    r = json.load(open("report.json"))
    every = max(1, int(os.environ.get("ALERT_EVERY_H", "6")))
    bucket = int(datetime.now(timezone.utc).timestamp() // (every * 3600))
    basis = json.dumps(sorted((x["kind"], x["subject"]) for x in r["findings"]))
    print(f"wa-monitor-{hashlib.sha256(basis.encode()).hexdigest()[:16]}-{bucket}", end="")


if __name__ == "__main__":
    if sys.argv[1:] == ["key"]:
        key()
        sys.exit(0)
    try:
        sys.exit(check())
    except Exception as e:  # never report healthy when the check itself broke
        print(f"the WhatsApp health check broke: {e}")
        sys.exit(2)
