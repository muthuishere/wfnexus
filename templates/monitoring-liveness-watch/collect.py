#!/usr/bin/env python3
"""collect.py — read-only production signals -> deterministic candidates.

One JSON document on stdout: {sources, healthy, candidates[]}. Every candidate
comes from a RULE over a number or a row, never from a model; the agent after
this explains candidates and may not add one. Each carries a FINGERPRINT that
has no counts, times or ids in it (sha1(kind|key)), so a repeat run finds the
issue it already filed instead of opening a duplicate.

Sources — each optional, each isolated, so one broken source or one broken
check never blinds the others (and is itself reported as a candidate: silence
from a broken collector must never read as "healthy"):

  logs        LOKI_QUERY         LogQL for error lines; grouped by a normalised
                                 signature per LOKI_GROUP_LABEL.
  checks      CHECKS_DIR         one rule per file: *.promql, *.logql, *.sql
                                 (header format below). The data-driven part:
                                 adding a check is adding a file, not editing code.
  targets     PROM_TARGETS=1     Prometheus' own target list: down, or up but
                                 erroring (lastError set).
  metrics     EXPECTED_METRICS   file, one selector per line, each must have series.
              EXPORTER_QUERIES   glob of postgres_exporter-style queries.yaml; every
                                 GAUGE/COUNTER it declares is expected to exist.
  dashboards  DASHBOARDS         glob of Grafana dashboard JSON; a panel whose
                                 metrics do not exist, or whose query returns
                                 nothing, is a blind panel.
  heartbeats  HEARTBEATS         "workflow=max_minutes,..." — watcher workflows on
                                 this platform that stopped completing, or sit
                                 blocked on a person.
              HEARTBEAT_STATE    the value a watcher wrote to project state
                                 ("<iso time> sources=<ok>/<total>").

Transport (all GET or read-only SQL):
  OBS_SSH     ssh host; when set, Loki/Prometheus are asked on THAT host's loopback.
  LOKI_URL    default http://localhost:3100      PROM_URL  default http://localhost:9090
  DB_PSQL     a psql command line (e.g. `ssh db sudo -u postgres psql -d app` or
              `psql "$DATABASE_URL"`). Every check runs alone inside
              BEGIN READ ONLY ... ROLLBACK with a statement timeout.
  WFX_API     the platform API for heartbeats (default http://127.0.0.1:8090).

Check file header (lines at the top starting with `--` or `#`):
  title:      Jobs failing: {action} — {error}     ({column}/{label} from the row; {rows} = count)
  severity:   critical | error | warning           (default warning)
  component:  jobs
  key:        action,error    one candidate per distinct key; absent = one candidate per check
  raise:      rows | empty    rows (default): each returned row is a problem;
                              empty: the query returning NOTHING is the problem
  window:     60              minutes, for .logql (default WINDOW_MIN)
  note:       a fact the triage agent must know (e.g. "a delayed message is not stuck")
  cross_check: database       .promql only: if our own DB checks ran, the signal is
                              the EXPORTER's (monitoring blind), not the database's
A .sql check is ONE SELECT (no `;` inside); it is wrapped as a subquery.
`$WINDOW_MIN` in any check is replaced by the look-back window.

Exit: 0 healthy, 3 candidates raised; anything else means the collector itself
broke, and the workflow fails the run rather than reporting "healthy".
"""
import datetime as dt
import glob
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
import urllib.parse
import urllib.request
from collections import OrderedDict

WINDOW = int(os.environ.get("WINDOW_MIN") or 20)
OBS_SSH = os.environ.get("OBS_SSH", "").strip()
LOKI_URL = os.environ.get("LOKI_URL", "http://localhost:3100").rstrip("/")
PROM_URL = os.environ.get("PROM_URL", "http://localhost:9090").rstrip("/")
DB_PSQL = os.environ.get("DB_PSQL", "").strip()
# One multiplexed connection per run: dozens of queries, one ssh handshake.
SSH = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ControlMaster=auto",
       "-o", "ControlPath=/tmp/wfx-cm-%C", "-o", "ControlPersist=60"]

candidates = []
sources = OrderedDict()
by_fp = {}


def fp(*parts):
    return hashlib.sha1("|".join(parts).encode()).hexdigest()[:12]


# Private addresses do not belong in an issue tracker; the fingerprint is taken
# from the raw key first, so redaction never merges two different problems.
IPV4 = re.compile(r"\b\d{1,3}(?:\.\d{1,3}){3}\b")
REDACT = os.environ.get("REDACT_IPS", "1") not in ("0", "false", "no")


def scrub(s):
    return IPV4.sub("<ip>", s) if REDACT else s


def add(kind, key, title, severity, evidence, **extra):
    f = fp(kind, key)
    title = scrub(title)
    evidence = [scrub(str(e)) for e in evidence]
    if f in by_fp:  # same problem seen through a second series/row: merge evidence
        c = by_fp[f]
        c["evidence"] = (c["evidence"] + [e for e in evidence if e not in c["evidence"]])[:12]
        return
    c = {"fingerprint": f, "kind": kind, "title": title[:180], "severity": severity,
         "evidence": [str(e)[:600] for e in evidence][:12], **extra}
    by_fp[f] = c
    candidates.append(c)


# ---------------------------------------------------------------- transport
def http_get(base, path, params, timeout=30):
    """GET only. Over ssh when OBS_SSH is set, so no observability port has to
    be public. Returns parsed JSON, or raises with a short reason."""
    url = base + path
    if OBS_SSH:
        cmd = "curl -sS -m %d -G %s" % (timeout, shlex.quote(url))
        for k, v in params.items():
            cmd += " --data-urlencode " + shlex.quote(f"{k}={v}")
        r = subprocess.run(SSH + [OBS_SSH, cmd], capture_output=True, text=True, timeout=timeout + 15)
        if r.returncode != 0:
            raise RuntimeError((r.stderr or "ssh/curl failed").strip()[:200])
        body = r.stdout
    else:
        q = urllib.parse.urlencode(params)
        with urllib.request.urlopen(url + ("?" + q if q else ""), timeout=timeout) as resp:
            body = resp.read().decode()
    try:
        return json.loads(body)
    except Exception:
        raise RuntimeError("not JSON: " + body.strip()[:160])


def prom(query):
    d = http_get(PROM_URL, "/api/v1/query", {"query": query})
    if d.get("status") != "success":
        raise RuntimeError(d.get("error", "query failed")[:200])
    return d["data"]["result"]


def loki_range(query, minutes, limit=2000):
    d = http_get(LOKI_URL, "/loki/api/v1/query_range",
                 {"query": query, "since": f"{minutes}m", "limit": str(limit), "direction": "backward"})
    if d.get("status") != "success":
        raise RuntimeError(d.get("error", "query failed")[:200])
    return d["data"]["result"]


# Functions that change something even inside a READ ONLY transaction. A check
# is somebody's file; the collector does not trust it to be harmless.
SQL_DENY = re.compile(r"(?i)\b(pg_terminate_backend|pg_cancel_backend|pg_reload_conf|pg_rotate_logfile|"
                      r"set_config|pg_advisory|lo_import|lo_export|dblink|pg_notify|pg_sleep|copy\s)")


def sql_rows(sql):
    body = sql.strip().rstrip(";").strip()
    if ";" in body:
        raise RuntimeError("a check must be ONE SELECT with no ';' inside")
    if SQL_DENY.search(body):
        raise RuntimeError("refused: calls a function with side effects outside the transaction")
    script = ("BEGIN READ ONLY;\nSET LOCAL statement_timeout = '20s';\n"
              f"SELECT coalesce(json_agg(q), '[]') FROM (\n{body}\n) q;\nROLLBACK;\n")
    r = subprocess.run(DB_PSQL + " -X -q -At -v ON_ERROR_STOP=1", shell=True, input=script,
                       capture_output=True, text=True, timeout=60)
    out = "\n".join(l for l in r.stdout.splitlines() if l.strip() not in ("BEGIN", "ROLLBACK", "SET"))
    if r.returncode != 0 or not out.strip():
        err = (r.stderr or "").strip().splitlines()
        raise RuntimeError(err[-1][:200] if err else f"psql exited {r.returncode} with no output")
    return json.loads(out)


# ---------------------------------------------------------------- helpers
class Blank(dict):
    def __missing__(self, k):
        return "?"


def fmt(template, row, n):
    try:
        return template.format_map(Blank({**{k: ("" if v is None else v) for k, v in row.items()}, "rows": n}))
    except Exception:
        return template


def header(path):
    h = {}
    with open(path) as f:
        for line in f:
            s = line.strip()
            if not s:
                continue
            if not (s.startswith("--") or s.startswith("#")):
                break
            m = re.match(r"^(?:--|#)\s*([a-z_]+):\s*(.*)$", s)
            if m:
                h[m.group(1)] = m.group(2).strip()
    return h


def body_of(path):
    return "\n".join(l for l in open(path).read().splitlines()
                     if not l.strip().startswith(("--", "#")) or not l.strip()).strip()


def series_label(m):
    name = m.get("__name__", "")
    labels = ",".join(f'{k}="{v}"' for k, v in sorted(m.items()) if k != "__name__")
    return f"{name}{{{labels}}}"


# ---------------------------------------------------------------- logs
NORMALISE = [
    (re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", re.I), "<uuid>"),
    (re.compile(r"\b[0-9a-f]{16,}\b", re.I), "<hex>"),
    (re.compile(r"(requestId|userId|request_id|user_id|trace_id|span_id)=\S+"), r"\1=<id>"),
    (re.compile(r"\d{4}-\d{2}-\d{2}[T ][\d:.]+Z?"), "<ts>"),
    (re.compile(r"elapsed=\S+"), "elapsed=<t>"),
    (re.compile(r"\b\d+(\.\d+)?(ms|s|µs)?\b"), "<n>"),
    (re.compile(r"\"[^\"]{24,}\""), '"<str>"'),
]


def signature(line):
    try:  # structured JSON logs: the message and error are the signature
        j = json.loads(line)
        line = " ".join(str(j.get(k, "")) for k in ("msg", "message", "error", "err", "handler") if j.get(k))
    except Exception:
        pass
    for rx, rep in NORMALISE:
        line = rx.sub(rep, line)
    return re.sub(r"\s+", " ", line).strip()[:220]


def collect_logs():
    q = os.environ.get("LOKI_QUERY", "").strip()
    if not q:
        return
    label = os.environ.get("LOKI_GROUP_LABEL", "container")
    strip = os.environ.get("LOKI_GROUP_STRIP", "")
    try:
        streams = loki_range(q, WINDOW)
    except Exception as e:
        sources["logs"] = f"unavailable: {e}"
        add("collector", "loki", "Log source (Loki) could not be read", "warning", [sources["logs"]], component="monitoring")
        return
    groups = OrderedDict()
    for s in streams:
        who = s["stream"].get(label, "?")
        if strip:
            who = re.sub(strip, "", who)
        for _ts, line in s["values"]:
            g = groups.setdefault((who, signature(line)), {"count": 0, "hosts": set(), "samples": []})
            g["count"] += 1
            g["hosts"].add(s["stream"].get("host", "?"))
            if len(g["samples"]) < 2:
                g["samples"].append(line[:600])
    sources["logs"] = f"ok: {sum(g['count'] for g in groups.values())} error lines, {len(groups)} distinct"
    for (who, sig), g in sorted(groups.items(), key=lambda kv: -kv[1]["count"])[:25]:
        add("log_error", who + "|" + sig, f"{who}: {sig[:120]}",
            "error" if re.search(r"(?i)panic|fatal", sig) else "warning",
            g["samples"], component=who, occurrences=g["count"], hosts=sorted(g["hosts"]))


# ---------------------------------------------------------------- checks
def rows_to_candidates(name, h, rows, render_row):
    title = h.get("title", name.replace("_", " "))
    sev = h.get("severity", "warning")
    comp = h.get("component", "app")
    extra = {"component": comp, "check": name}
    if h.get("note"):
        extra["note"] = h["note"]
    if h.get("raise", "rows") == "empty":
        if not rows:
            add(name, "empty", fmt(title, {}, 0), sev, [h.get("expect", "the query returned nothing")], **extra)
        return
    if not rows:
        return
    keys = [k.strip() for k in h.get("key", "").split(",") if k.strip()]
    if not keys:
        add(name, "", fmt(title, rows[0], len(rows)), sev, [render_row(r) for r in rows[:8]], rows=len(rows), **extra)
        return
    grouped = OrderedDict()
    for r in rows:
        grouped.setdefault("|".join(str(r.get(k, "")) for k in keys), []).append(r)
    for key, rs in grouped.items():
        add(name, key, fmt(title, rs[0], len(rs)), sev, [render_row(r) for r in rs[:8]], rows=len(rs), **extra)


def run_checks(db_ok_holder):
    d = os.environ.get("CHECKS_DIR", "checks")
    files = sorted(glob.glob(os.path.join(d, "*.sql")) + glob.glob(os.path.join(d, "*.promql"))
                   + glob.glob(os.path.join(d, "*.logql")))
    ran = {"sql": [0, 0], "promql": [0, 0], "logql": [0, 0]}
    deferred = []
    for path in files:  # SQL first: a promql cross_check needs to know whether the DB answered
        if path.endswith(".sql"):
            deferred.insert(0, path)
        else:
            deferred.append(path)
    for path in deferred:
        name, ext = os.path.splitext(os.path.basename(path))
        ext = ext[1:]
        h = header(path)
        q = body_of(path).replace("$WINDOW_MIN", str(WINDOW))
        ran[ext][1] += 1
        try:
            if ext == "sql":
                if not DB_PSQL:
                    raise RuntimeError("DB_PSQL is not set")
                rows = sql_rows(q)
                db_ok_holder[0] = True
                rows_to_candidates(name, h, rows, lambda r: json.dumps(r, default=str)[:400])
            elif ext == "promql":
                res = prom(q)
                rows = [{**r["metric"], "value": r["value"][1], "_series": series_label(r["metric"])} for r in res]
                if h.get("cross_check") == "database" and rows and h.get("raise", "rows") == "rows":
                    if db_ok_holder[0]:
                        h = {**h, "title": "Monitoring blind: " + h.get("title", name) + " — but direct read-only queries to the database succeed",
                             "severity": "error", "component": "monitoring",
                             "note": (h.get("note", "") + " The database answered our own checks, so the broken part is the exporter/scrape, not the database.").strip()}
                    else:
                        h = {**h, "severity": "critical", "component": "database",
                             "title": "Database unreachable: " + h.get("title", name) + " and direct queries fail too"}
                rows_to_candidates(name, h, rows, lambda r: f"{r['_series']} = {r['value']}")
            else:
                streams = loki_range(q, int(h.get("window") or WINDOW), limit=500)
                rows = []
                for s in streams:
                    for _ts, line in s["values"][:5]:
                        rows.append({**s["stream"], "line": line[:300]})
                rows_to_candidates(name, h, rows, lambda r: r.get("line", json.dumps(r)[:300]))
            ran[ext][0] += 1
        except Exception as e:
            add("check_broken", name, f"Check '{name}' could not run — that signal is blind", "warning",
                [f"{os.path.basename(path)}: {e}"], component="monitoring", check=name)
    for ext, (ok, total) in ran.items():
        if total:
            sources[f"checks.{ext}"] = f"{ok}/{total} ran"


# ---------------------------------------------------------------- targets
def collect_targets():
    if os.environ.get("PROM_TARGETS", "") not in ("1", "true", "yes"):
        return
    try:
        d = http_get(PROM_URL, "/api/v1/targets", {"state": "active"})
        ts = d["data"]["activeTargets"]
    except Exception as e:
        sources["targets"] = f"unavailable: {e}"
        add("collector", "prometheus", "Metrics source (Prometheus) could not be read", "error",
            [sources["targets"]], component="monitoring")
        return
    bad = 0
    for t in ts:
        err = (t.get("lastError") or "").strip()
        if t.get("health") == "up" and not err:
            continue
        bad += 1
        l = t.get("labels", {})
        add("target_unhealthy", f"{l.get('job')}|{l.get('instance')}",
            f"Scrape target {l.get('job')} {l.get('instance')} is {t.get('health')}", "error",
            [f"health={t.get('health')} lastScrape={t.get('lastScrape')}", f"lastError: {err or '(none)'}"],
            component="monitoring")
    sources["targets"] = f"ok: {len(ts)} targets, {bad} unhealthy"


# ---------------------------------------------------------------- expected metrics
def exporter_metrics(pattern):
    """GAUGE/COUNTER columns of every postgres_exporter-style queries.yaml —
    the exporter names them <top-level key>_<column>."""
    out = []
    for path in sorted(glob.glob(pattern)):
        ns, col = None, None
        for line in open(path):
            m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*):\s*$", line)
            if m:
                ns = m.group(1)
                continue
            m = re.match(r"^\s*-\s*([A-Za-z_][A-Za-z0-9_]*):\s*$", line)
            if m:
                col = m.group(1)
                continue
            m = re.match(r'^\s*usage:\s*"?(GAUGE|COUNTER|HISTOGRAM|MAPPEDMETRIC|DURATION)"?', line)
            if m and ns and col:
                out.append((f"{ns}_{col}", f"{os.path.basename(path)}: {ns}.{col}"))
                col = None
    return out


_exists = {}


def existing(names):
    """Which of these metric names have at least one series — one query per
    batch of names instead of one per name."""
    todo = sorted({n for n in names if n not in _exists})
    for i in range(0, len(todo), 60):
        batch = todo[i:i + 60]
        present = {r["metric"].get("__name__") for r in
                   prom('count by (__name__) ({__name__=~"%s"})' % "|".join(re.escape(n) for n in batch))}
        for n in batch:
            _exists[n] = n in present
    return {n for n in names if _exists.get(n)}


def collect_expected_metrics():
    wanted = []
    f = os.environ.get("EXPECTED_METRICS", "")
    if f and os.path.exists(f):
        for line in open(f):
            s = line.split("#", 1)[0].strip()
            if s:
                wanted.append((s, f"{os.path.basename(f)}"))
    if os.environ.get("EXPORTER_QUERIES"):
        wanted += exporter_metrics(os.environ["EXPORTER_QUERIES"])
    if not wanted:
        return
    missing = []
    try:
        plain = [sel for sel, _ in wanted if re.fullmatch(r"[A-Za-z_:][A-Za-z0-9_:]*", sel)]
        have = existing(plain)
        for sel, why in wanted:
            if sel in plain:
                if sel not in have:
                    missing.append((sel, why))
            elif not prom(f"count({sel})"):
                missing.append((sel, why))
    except Exception as e:
        sources["expected_metrics"] = f"unavailable: {e}"
        return
    sources["expected_metrics"] = f"ok: {len(wanted)} expected, {len(missing)} missing"
    # One candidate per SOURCE file: twelve missing pg metrics are one broken
    # exporter, not twelve issues.
    per = OrderedDict()
    for sel, why in missing:
        per.setdefault(why.split(":")[0], []).append((sel, why))
    for src, items in per.items():
        add("metrics_missing", src, f"{len(items)} expected metric(s) have no series in Prometheus ({src})",
            "error", [f"absent: {sel}  (declared by {why})" for sel, why in items], component="monitoring",
            metrics=[s for s, _ in items])


# ---------------------------------------------------------------- dashboards
PROMQL_WORDS = set("""sum min max avg count stddev stdvar topk bottomk quantile count_values group by without on
ignoring group_left group_right bool and or unless offset rate irate increase delta idelta deriv predict_linear
histogram_quantile abs ceil floor round clamp clamp_min clamp_max exp ln log2 log10 sqrt time timestamp vector scalar
absent absent_over_time avg_over_time min_over_time max_over_time sum_over_time count_over_time last_over_time
quantile_over_time stddev_over_time changes resets label_replace label_join sort sort_desc day_of_month day_of_week
days_in_month hour minute month year sgn inf nan""".split())


def metric_names(expr):
    e = re.sub(r'"(?:[^"\\]|\\.)*"', '""', expr)          # string literals
    e = re.sub(r"\{[^}]*\}", "", e)                        # label matchers
    e = re.sub(r"\[[^\]]*\]", "", e)                       # ranges
    e = re.sub(r"\b(by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)", "", e)
    names = set()
    for m in re.finditer(r"[A-Za-z_:][A-Za-z0-9_:]*", e):
        w = m.group(0)
        if w.lower() in PROMQL_WORDS or "_" not in w or w.startswith("$"):
            continue
        names.add(w)
    return names


def substitute_vars(expr):
    expr = re.sub(r"\$__(range|rate_interval|interval|range_s|interval_ms)\b", "5m", expr)
    expr = re.sub(r'(\w+)\s*=\s*"\$\{?\w+\}?"', r'\1=~".*"', expr)
    expr = re.sub(r"\$\{?\w+(:\w+)?\}?|\[\[\w+\]\]", ".*", expr)
    return expr


def panels(d):
    for p in d.get("panels", []) or []:
        yield p
        yield from panels(p)
    for r in d.get("rows", []) or []:
        yield from panels(r)


def collect_dashboards():
    pattern = os.environ.get("DASHBOARDS", "")
    if not pattern:
        return
    files = sorted(glob.glob(pattern, recursive=True))
    if not files:
        sources["dashboards"] = f"unavailable: no files match {pattern}"
        add("collector", "dashboards", "Dashboards to verify were not found", "warning",
            [sources["dashboards"]], component="monitoring")
        return
    checked = dead_total = 0
    try:
        for path in files:
            d = json.load(open(path))
            d = d.get("dashboard", d)
            title = d.get("title") or os.path.basename(path)
            dead = []
            for p in panels(d):
                for t in p.get("targets", []) or []:
                    expr = t.get("expr")
                    ds = t.get("datasource") or p.get("datasource") or {}
                    dstype = ds.get("type", "prometheus") if isinstance(ds, dict) else str(ds)
                    if not expr or "loki" in dstype.lower():
                        continue
                    checked += 1
                    q = substitute_vars(expr)
                    names = sorted(metric_names(q))
                    have = existing(names)
                    absent = [n for n in names if n not in have]
                    masked = bool(re.search(r"(?i)\bor\s+on\s*\(\s*\)\s*vector\(", q))
                    if absent:
                        dead.append(f"panel '{p.get('title')}': metric(s) {', '.join(absent)} do not exist"
                                    + (" — and `or on() vector(0)` makes the panel show 0 instead of 'No data', hiding it" if masked else ""))
                        continue
                    if not masked and not prom(q):
                        dead.append(f"panel '{p.get('title')}': query returns no series ({expr[:140]})")
            if dead:
                dead_total += len(dead)
                add("dashboard_blind", title, f"Dashboard '{title}': {len(dead)} panel(s) show no real data",
                    "warning", dead, component="monitoring", dashboard=os.path.relpath(path))
    except Exception as e:
        sources["dashboards"] = f"unavailable: {e}"
        add("collector", "dashboards", "Dashboards could not be verified", "warning", [str(e)[:200]], component="monitoring")
        return
    sources["dashboards"] = f"ok: {len(files)} dashboard(s), {checked} queries, {dead_total} blind"


# ---------------------------------------------------------------- heartbeats
def parse_ts(s):
    return dt.datetime.fromisoformat(s.replace("Z", "+00:00"))


def collect_heartbeats():
    spec = os.environ.get("HEARTBEATS", "").strip()
    now = dt.datetime.now(dt.timezone.utc)
    state = os.environ.get("HEARTBEAT_STATE", "").strip()
    if state and "<no value>" not in state:
        try:
            t = parse_ts(state.split()[0])
            age = (now - t).total_seconds() / 60
            limit = float(os.environ.get("HEARTBEAT_STATE_MAX_MIN") or 60)
            bits = dict(p.split("=", 1) for p in state.split()[1:] if "=" in p)
            if age > limit:
                add("heartbeat_stale", "state", f"Watcher heartbeat is {age:.0f} minutes old (limit {limit:.0f})", "error",
                    [f"last heartbeat: {state}"], component="monitoring")
            ok, _, total = bits.get("sources", "").partition("/")
            if ok and total and ok != total:
                add("heartbeat_degraded", "state", f"Watcher runs, but only {ok} of {total} of its sources answer",
                    "error", [f"last heartbeat: {state}"], component="monitoring")
        except Exception as e:
            add("heartbeat_stale", "state", "Watcher heartbeat is unreadable", "warning", [f"{state[:120]}: {e}"], component="monitoring")
    elif os.environ.get("HEARTBEAT_STATE_REQUIRED", "") in ("1", "true"):
        add("heartbeat_stale", "state", "Watcher has never written a heartbeat", "error",
            ["project state holds no heartbeat — the watcher never completed a collect step, or does not write one"],
            component="monitoring")
    if not spec:
        return
    api = os.environ.get("WFX_API", "http://127.0.0.1:8090").rstrip("/")
    try:
        with urllib.request.urlopen(api + "/api/runs?limit=500", timeout=20) as r:
            runs = json.loads(r.read().decode())
    except Exception as e:
        sources["heartbeats"] = f"unavailable: {e}"
        add("collector", "wfx-api", "Run history could not be read, so watcher heartbeats are unknown", "warning",
            [str(e)[:200]], component="monitoring")
        return
    notes = []
    for part in spec.split(","):
        name, _, lim = part.strip().partition("=")
        limit = float(lim or 60)
        mine = [r for r in runs if r.get("workflow") == name or r.get("workflow", "").endswith("/" + name)]
        done = [r for r in mine if r.get("status") == "done"]
        last = max((parse_ts(r["updatedAt"]) for r in done), default=None)
        age = (now - last).total_seconds() / 60 if last else None
        notes.append(f"{name}: last done {'never' if last is None else f'{age:.0f}m ago'}")
        if age is None or age > limit:
            ev = [f"last completed run: {last.isoformat() if last else 'never'} (limit {limit:.0f} minutes)"]
            ev += [f"latest run {r['id'][:8]} status={r['status']} step={r.get('currentStep') or '-'} created={r['createdAt']}"
                   for r in sorted(mine, key=lambda r: r["createdAt"], reverse=True)[:3]]
            add("watcher_silent", name, f"Watcher '{name}' has not completed a run in {limit:.0f} minutes", "error", ev,
                component="monitoring")
        for r in mine:
            if r.get("status") in ("needs_input", "awaiting_approval", "running"):
                held = (now - parse_ts(r["createdAt"])).total_seconds() / 60
                if held > limit:
                    add("watcher_blocked", name, f"Watcher '{name}' has a run held for {held/60:.1f}h ({r['status']} at {r.get('currentStep') or '?'})",
                        "warning", [f"run {r['id']} status={r['status']} step={r.get('currentStep')} since {r['createdAt']}",
                                    "a watcher waiting on a person is a watcher not watching, if its schedule skips while a run is open"],
                        component="monitoring")
    sources["heartbeats"] = "ok: " + "; ".join(notes)


# ---------------------------------------------------------------- main
def main():
    db_ok = [False]
    collect_logs()
    run_checks(db_ok)
    collect_targets()
    collect_expected_metrics()
    collect_dashboards()
    collect_heartbeats()
    if not sources:
        print("no source is configured: set LOKI_QUERY, CHECKS_DIR, PROM_TARGETS, EXPECTED_METRICS, DASHBOARDS or HEARTBEATS",
              file=sys.stderr)
        sys.exit(2)
    def healthy_source(v):
        m = re.match(r"(\d+)/(\d+) ran", str(v))
        return m.group(1) == m.group(2) if m else not str(v).startswith("unavailable")
    ok = sum(1 for v in sources.values() if healthy_source(v))
    json.dump({"window_min": WINDOW, "sources": sources, "sources_ok": f"{ok}/{len(sources)}",
               "healthy": not candidates, "candidate_count": len(candidates), "candidates": candidates},
              sys.stdout, indent=1, default=str)
    print()
    sys.exit(0 if not candidates else 3)


if __name__ == "__main__":
    main()
