#!/usr/bin/env python3
"""dossier.py — everything a judge needs to decide whether a finished run did its job.

Usage: dossier.py <run_id> [api_base]

Reads the run from the platform API (run, steps with outputs, the definition)
and its activity log (`wfx logs <id>`, not followed), and writes:

  dossier.md     the readable dossier the model judges read
  dossier.jsonl  one {"id", "state"} line for the calibrated classifier (JEV)
  run.json       the raw API response, for anyone who wants to check a claim

The dossier is FACTS, not opinions: the workflow's stated job, each step's
status/output/error, approvals and questions, what it filed or produced, and
whether those outputs actually exist (a GitHub issue URL is looked up with gh).
Anything that looks like a credential is redacted before it is written — a
dossier is read by three outside models.

Exit codes: 0 dossier written · 2 the run could not be read (the eval stops
here: there is nothing to judge) · 4 the run has not finished yet.
"""
import base64
import json
import re
import subprocess
import sys
import urllib.request

run_id = sys.argv[1].strip()
api = (sys.argv[2] if len(sys.argv) > 2 else "http://127.0.0.1:8090").rstrip("/")

if not re.fullmatch(r"[0-9a-fA-F-]{8,64}", run_id):
    print(f"run_id {run_id!r} is not a run id")
    sys.exit(2)

SECRET_PATTERNS = [
    re.compile(r"(sk|pk|rk|xai|gsk|ghp|gho|ghs|github_pat)[-_][A-Za-z0-9_\-]{16,}"),
    re.compile(r"(?i)(api[_-]?key|token|secret|password|passwd|authorization)(\s*[:=]\s*|\s+bearer\s+)[\"']?[^\s\"',]{8,}"),
    re.compile(r"(?i)bearer\s+[A-Za-z0-9._\-]{16,}"),
    re.compile(r"postgres(ql)?://[^:\s]+:[^@\s<]+@"),
]


def redact(text):
    text = text or ""
    for p in SECRET_PATTERNS:
        text = p.sub("[redacted]", text)
    return text


def clip(text, n):
    text = text or ""
    return text if len(text) <= n else text[:n] + f" …[{len(text) - n} more chars]"


try:
    with urllib.request.urlopen(f"{api}/api/runs/{run_id}", timeout=30) as r:
        raw = r.read().decode()
    data = json.loads(raw)
except Exception as e:  # the eval cannot judge a run it cannot read
    print(f"could not read run {run_id} from {api}: {e}")
    sys.exit(2)

with open("run.json", "w") as f:
    f.write(redact(raw))

run = data.get("run") or {}
steps = data.get("steps") or []
defn = data.get("definition") or {}
arts = data.get("artifacts") or []

if run.get("status") in ("running", "queued", "pending"):
    print(f"run {run_id} is still {run.get('status')}; evaluate it once it has finished")
    sys.exit(4)

try:
    logs = subprocess.run(["wfx", "logs", run_id], capture_output=True, text=True, errors="replace", timeout=60).stdout
except Exception as e:
    logs = f"(wfx logs failed: {e})"
logs = redact(logs)
log_lines = logs.splitlines()
flagged = [l for l in log_lines if re.search(r"(?i)\b(error|fail|failed|panic|denied|refused|timeout|uncertain|warn)", l)]

# ---- what the run claims to have produced, and whether it exists ------------
all_out = json.dumps([s.get("output") for s in steps])
urls = sorted(set(re.findall(r"https://github\.com/[\w.-]+/[\w.-]+/(?:issues|pull)/\d+", all_out)))
verified = []
for u in urls[:20]:
    try:
        r = subprocess.run(["gh", "issue" if "/issues/" in u else "pr", "view", u, "--json", "number,title,state,labels,createdAt"],
                           capture_output=True, text=True, timeout=30)
        if r.returncode == 0:
            v = json.loads(r.stdout)
            labels = ",".join(l["name"] for l in v.get("labels", []))
            verified.append(f"{u} — EXISTS · {v.get('state')} · labels [{labels}] · created {v.get('createdAt')} · \"{v.get('title')}\"")
        else:
            verified.append(f"{u} — COULD NOT VERIFY: {clip(r.stderr.strip(), 160)}")
    except Exception as e:
        verified.append(f"{u} — COULD NOT VERIFY: {e}")

# ---- what the run wrote in its workspace -------------------------------------
# A step's stdout is often a summary ("2 uncertain"); the per-item facts it
# acted on (verdicts.jsonl, uncertain.json, triage.json) are files at the
# workspace root. Judging without them means judging the summary. Only small
# data files at the ROOT, written after the run started, are read — never the
# run's secrets directory, never the repository's own files.
import datetime as _dt
import glob
import os

work_root = os.path.dirname(os.path.dirname(os.environ.get("WFX_WORKSPACE", ""))) or os.path.expanduser("~/wfnexus-local/work")
run_files = []
try:
    started = _dt.datetime.fromisoformat((run.get("startedAt") or "").replace("Z", "+00:00")).timestamp()
except Exception:
    started = 0
for ws in ("worktree", "repo"):
    base = os.path.join(work_root, run_id, ws)
    for p in sorted(glob.glob(os.path.join(base, "*"))):
        name = os.path.basename(p)
        if not os.path.isfile(p) or not name.endswith((".json", ".jsonl")) or "secret" in name.lower():
            continue
        if os.path.getmtime(p) < started or os.path.getsize(p) > 8000:
            continue
        try:
            run_files.append((name, redact(open(p, errors="replace").read())))
        except Exception:
            pass
    if run_files:
        break

# ---- the definition's own small sidecars (questions, rubrics) ----------------
sidecars = []
for fi in defn.get("files") or []:
    path, body = fi.get("path", ""), fi.get("body", "")
    if path.endswith((".yaml", ".yml", ".md", ".json")) and fi.get("size", 0) <= 3000:
        try:
            sidecars.append((path, redact(base64.b64decode(body).decode("utf-8", "replace"))))
        except Exception:
            pass

# ---- the dossier --------------------------------------------------------------
def_steps = {s.get("id"): s for s in defn.get("steps") or []}
md = []
md.append(f"# Dossier — run {run_id}\n")
md.append(f"- workflow: **{run.get('workflow')}** (project {run.get('project')})")
md.append(f"- status: **{run.get('status')}**" + (f" — run error: {redact(run.get('error'))}" if run.get("error") else ""))
md.append(f"- started {run.get('startedAt')} · last update {run.get('updatedAt')}")
md.append(f"- input: `{json.dumps(run.get('input'))}`")
md.append("\n## The workflow's stated job\n")
md.append(redact(defn.get("description") or "(no description)"))
if sidecars:
    md.append("\n## Its own rules (sidecar files)\n")
    for p, b in sidecars:
        md.append(f"### {p}\n```\n{clip(b, 2500)}\n```")
md.append("\n## Steps, in order\n")
for s in sorted(steps, key=lambda x: x.get("position", 0)):
    sid = s.get("stepId")
    d = def_steps.get(sid, {})
    kind = "run" if d.get("run") else ("judge" if d.get("judge") else "agent")
    md.append(f"### {s.get('position', 0) + 1}. {sid} — {s.get('status')} ({kind}{', provider ' + d['provider'] if d.get('provider') else ''})")
    if d.get("description"):
        md.append(f"_intended_: {d['description']}")
    if d.get("when"):
        md.append(f"_runs only when_: `{json.dumps(d['when'])}`")
    if d.get("requiresApproval"):
        md.append("_requires human approval before running_")
    if s.get("error"):
        md.append(f"**error**: {clip(redact(s['error']), 800)}")
    if s.get("pending"):
        md.append(f"**asked a human**: {clip(redact(json.dumps(s['pending'])), 600)}")
    if s.get("resolution") or s.get("resolvedBy"):
        md.append(f"**human resolution**: {s.get('resolution')} by {s.get('resolvedBy')} — {s.get('resolutionReason')}")
    if s.get("decision"):
        md.append(f"**classifier decision**: {clip(json.dumps(s['decision']), 800)}")
    out = s.get("output")
    if out is not None:
        md.append("output:\n```json\n" + clip(redact(json.dumps(out, indent=1, ensure_ascii=False)), 8000) + "\n```")
    if s.get("rawText"):
        md.append("agent's final words:\n> " + clip(redact(s["rawText"]), 1200).replace("\n", "\n> "))
    md.append("")
md.append("## What it filed or produced\n")
if verified:
    md.extend(f"- {v}" for v in verified)
else:
    md.append("- no GitHub issue or PR links in any step output")
if run_files:
    md.append("\n## Data files the run wrote in its workspace (what its steps actually acted on)\n")
    for name, body in run_files:
        md.append(f"### {name}\n```\n{clip(body, 4000)}\n```")
elif steps:
    md.append("- (the run's workspace files could not be read from this machine — judge step summaries with care)")
if arts:
    md.append("- artifacts: " + ", ".join(f"{a.get('stepId')}/{a.get('name')} ({a.get('sizeBytes')}B)" for a in arts))
md.append("\n## Activity log — lines that mention errors, failures or uncertainty\n")
md.append("```\n" + clip("\n".join(flagged[-60:]) or "(none)", 5000) + "\n```")
md.append("\n## Activity log — last 25 lines\n")
md.append("```\n" + clip("\n".join(log_lines[-25:]), 4000) + "\n```")
dossier = "\n".join(md)
with open("dossier.md", "w") as f:
    f.write(dossier)

# The classifier gets a compact version: the job, each step's facts, the proof.
compact = [f"Workflow {run.get('workflow')} — stated job: {defn.get('description')}",
           f"Run status: {run.get('status')}. Input: {json.dumps(run.get('input'))}."]
for s in sorted(steps, key=lambda x: x.get("position", 0)):
    o = json.dumps(s.get("output"), ensure_ascii=False) if s.get("output") is not None else "no output"
    compact.append(f"Step {s.get('stepId')} [{s.get('status')}]" + (f" error: {s.get('error')}" if s.get("error") else "")
                   + f" output: {clip(o, 1400)}")
compact.append("Data files the run wrote: " + (" || ".join(f"{n}: {clip(b, 900)}" for n, b in run_files) if run_files else "not readable"))
compact.append("Filed/produced: " + ("; ".join(verified) if verified else "nothing linkable"))
compact.append("Log problems: " + clip(" | ".join(flagged[-15:]), 1500))
with open("dossier.jsonl", "w") as f:
    f.write(json.dumps({"id": run_id, "state": clip(redact("\n".join(compact)), 12000)}) + "\n")

print(f"dossier for {run.get('workflow')} run {run_id} ({run.get('status')}): "
      f"{len(steps)} steps, {len(verified)} linked outputs, {len(flagged)} flagged log lines, {len(dossier)} chars")
