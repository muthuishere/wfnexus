#!/usr/bin/env python3
"""scorecard.py — fold every judge's verdict into one scorecard.

Reads judges.json (written by the step: each model judge's probe and output,
the JEV step's output) and jev.jsonl (the classifier's answers), and writes
scorecard.md + scorecard.json. Prints scorecard.md.

The rule is deterministic on purpose — no model decides the overall verdict:

  PASS     JEV says did_the_job is `yes`  AND  at least two model judges pass
  FAIL     JEV says `no`, or at least two model judges fail
  PARTIAL  anything else (including too few judges available to pass)

A judge that did not report is listed as UNAVAILABLE with the reason. It never
counts as a pass, and it is never silently dropped.
"""
import json
import os
import sys

MODEL_JUDGES = [("gpt_oss", "gpt-oss"),
                ("codex", "gpt5"),
                ("grok", "haiku")]

run_id = sys.argv[1] if len(sys.argv) > 1 else "?"
try:
    workflow = json.load(open("run.json")).get("run", {}).get("workflow") or "?"
except Exception:
    workflow = "?"

try:
    J = json.load(open("judges.json"))
except Exception as e:
    J = {}
    print(f"judges.json unreadable: {e}", file=sys.stderr)


def clip(s, n=300):
    s = (s or "").strip().replace("\n", " ")
    return s if len(s) <= n else s[:n] + "…"


judges = {}  # name -> dict(status, verdict, did_the_job, evidence_backed, fp, missed, risks, rationale, reason)

# ---- JEV ---------------------------------------------------------------------
jev = {"label": "JEV", "status": "unavailable"}
answers = None
try:
    with open("jev.jsonl") as f:
        line = f.readline().strip()
    rec = json.loads(line) if line else {}
    answers = rec.get("answers")
    if not answers and rec.get("error"):
        jev["reason"] = clip(str(rec.get("error")))
except FileNotFoundError:
    jev["reason"] = "jev.jsonl was not written"
except Exception as e:
    jev["reason"] = f"jev.jsonl unreadable: {e}"
if answers:
    def noul(q):
        a = answers.get(q) or {}
        return a.get("noul"), a.get("band")
    dj, dj_band = noul("did_the_job")
    eb, eb_band = noul("evidence_backed")
    sa, sa_band = noul("would_a_senior_engineer_accept")
    worst = (answers.get("severity_of_worst_problem") or {}).get("choice")
    if dj_band == "yes" and sa_band != "no":
        v = "pass"
    elif dj_band == "no":
        v = "fail"
    else:
        v = "partial"
    jev.update(status="reported", verdict=v, did_the_job=dj, evidence_backed=eb,
               bands={"did_the_job": dj_band, "evidence_backed": eb_band, "senior_accepts": sa_band},
               senior_accepts=sa, worst=worst, false_positives=[], missed=[],
               risks=[f"worst problem (classifier): {worst}"] if worst and worst != "none" else [],
               rationale=(f"did_the_job {dj:.2f} ({dj_band}), evidence_backed {eb:.2f} ({eb_band}), "
                          f"senior would accept {sa:.2f} ({sa_band}), worst problem: {worst}.")
               if isinstance(dj, (int, float)) and isinstance(eb, (int, float)) and isinstance(sa, (int, float)) else "")
elif "reason" not in jev:
    step = J.get("jev") or {}
    jev["reason"] = clip((step.get("stderr") if isinstance(step, dict) else "") or "the classifier returned no answers")
judges["jev"] = jev

# ---- model judges ------------------------------------------------------------
for key, label in MODEL_JUDGES:
    entry = J.get(key) or {}
    probe, out = entry.get("probe"), entry.get("out")
    j = {"label": label}
    if isinstance(out, dict) and out.get("verdict"):
        j.update(status="reported", **{k: out.get(k) for k in
                 ("verdict", "did_the_job", "evidence_backed", "false_positives", "missed", "risks", "rationale")})
    else:
        j["status"] = "unavailable"
        if isinstance(probe, dict) and not probe.get("ok"):
            j["reason"] = "probe failed: " + clip(probe.get("stdout") or probe.get("stderr") or f"exit {probe.get('exitCode')}")
        elif not probe:
            j["reason"] = "probe did not run"
        else:
            j["reason"] = "the judge step produced no verdict"
    judges[key] = j

# ---- agreement ---------------------------------------------------------------
reported = {k: v for k, v in judges.items() if v["status"] == "reported"}
names = list(reported)
pairs = agree = 0
disagreements = []
for i in range(len(names)):
    for k in range(i + 1, len(names)):
        a, b = reported[names[i]], reported[names[k]]
        pairs += 1
        if a["verdict"] == b["verdict"]:
            agree += 1
        else:
            disagreements.append(f"{a['label']} says **{a['verdict']}**, {b['label']} says **{b['verdict']}**")
scores = [v["did_the_job"] for v in reported.values() if isinstance(v.get("did_the_job"), (int, float))]
spread = (max(scores) - min(scores)) if len(scores) > 1 else 0.0
if spread > 0.4:
    lo = min(reported.values(), key=lambda v: v.get("did_the_job") if isinstance(v.get("did_the_job"), (int, float)) else 9)
    hi = max(reported.values(), key=lambda v: v.get("did_the_job") if isinstance(v.get("did_the_job"), (int, float)) else -9)
    disagreements.append(f"did_the_job spread {spread:.2f}: {lo['label']} {lo['did_the_job']:.2f} vs {hi['label']} {hi['did_the_job']:.2f}")

# ---- overall -----------------------------------------------------------------
model_pass = sum(1 for k, _ in MODEL_JUDGES if judges[k].get("verdict") == "pass")
model_fail = sum(1 for k, _ in MODEL_JUDGES if judges[k].get("verdict") == "fail")
model_up = sum(1 for k, _ in MODEL_JUDGES if judges[k]["status"] == "reported")
jev_band = (judges["jev"].get("bands") or {}).get("did_the_job")
if jev_band == "yes" and model_pass >= 2:
    overall, why = "pass", f"JEV did_the_job is yes and {model_pass} of {model_up} model judges pass"
elif jev_band == "no" or model_fail >= 2:
    overall, why = "fail", (f"JEV did_the_job is no" if jev_band == "no" else "") + \
        (" and " if jev_band == "no" and model_fail >= 2 else "") + (f"{model_fail} model judges fail" if model_fail >= 2 else "")
else:
    bits = []
    if jev_band != "yes":
        bits.append(f"JEV did_the_job is {jev_band or 'unavailable'}")
    if model_pass < 2:
        bits.append(f"only {model_pass} of {model_up} available model judges pass")
    overall, why = "partial", "; ".join(bits)
if model_up < 2:
    why += f" — WARNING: only {model_up} model judge(s) reported, so this run cannot pass"

# ---- write -------------------------------------------------------------------
def fmt(x):
    return f"{x:.2f}" if isinstance(x, (int, float)) else "—"


md = [f"# Scorecard — {workflow} run {run_id}", "",
      f"**OVERALL: {overall.upper()}** — {why}.", "",
      f"Judges reporting: {len(reported)} of {len(judges)} · verdict agreement: "
      + (f"{agree}/{pairs} pairs" if pairs else "n/a"), "",
      "| judge | status | verdict | did_the_job | evidence_backed |", "|---|---|---|---|---|"]
for v in judges.values():
    md.append(f"| {v['label']} | {v['status']} | {v.get('verdict', '—')} | {fmt(v.get('did_the_job'))} | {fmt(v.get('evidence_backed'))} |")
md += ["", "## Disagreements", *(f"- {d}" for d in disagreements or ["none — the judges that reported agree"])]
unavail = [v for v in judges.values() if v["status"] != "reported"]
if unavail:
    md += ["", "## Unavailable judges", *(f"- {v['label']}: {v.get('reason', 'no reason recorded')}" for v in unavail)]
for field, title in (("missed", "What the run missed"), ("false_positives", "False positives"), ("risks", "Risks")):
    items = [(v["label"], x) for v in reported.values() for x in (v.get(field) or [])]
    if items:
        md += ["", f"## {title}", *(f"- ({lbl}) {clip(str(x), 400)}" for lbl, x in items)]
md += ["", "## Each judge's rationale", *(f"- **{v['label']}** — {clip(v.get('rationale'), 900)}" for v in reported.values())]
text = "\n".join(md) + "\n"
open("scorecard.md", "w").write(text)
json.dump({"run_id": run_id, "workflow": workflow, "overall": overall, "why": why, "judges": judges,
           "agreement": {"pairs": pairs, "agree": agree}, "disagreements": disagreements},
          open("scorecard.json", "w"), indent=1, default=str)
print(text if len(text) <= 3900 else text[:3850] + "\n…(full text in scorecard.md)")
