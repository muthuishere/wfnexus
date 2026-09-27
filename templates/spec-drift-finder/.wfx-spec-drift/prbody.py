#!/usr/bin/env python3
"""prbody.py — the PR body, written by code from the run's files, so no number in it is invented.

Sections: what was checked (counts by verdict, per spec file), the table the
owner asked for — requirement -> verdict -> decision -> change — for every
non-conforming requirement, how each decision was made (who, why), the proof
(suite before/after, re-judge verdicts, any problem a person accepted), the
reviewer's verdict, and every conforming requirement collapsed at the end.

Env: TITLE_PREFIX, REVIEW (the reviewer's verdict text). Writes pr-body.md and pr-title.txt.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D, load_jsonl  # noqa: E402


def cell(s, n=140):
    return str(s or "").replace("|", "/").replace("\n", " ")[:n]


def main():
    prefix = os.environ.get("TITLE_PREFIX") or "[spec-drift]"
    plan = json.load(open(f"{D}/plan.json"))["plan"]
    rows = {r["id"]: r for r in json.load(open(f"{D}/decisions.json"))["decisions"]}
    acts = {a["id"]: a for a in load_jsonl(f"{D}/act.jsonl") if a.get("id")}
    ver = json.load(open(f"{D}/verify.json"))
    specs = json.load(open(f"{D}/specs.json"))
    commits = {}
    for c in ver["commits"]:
        if c["id"]:
            commits.setdefault(c["id"], []).append(c["sha"])
    rej = {r["id"]: r for r in ver["rejudge"]}

    counts = {}
    for p in plan:
        counts[p["verdict"]] = counts.get(p["verdict"], 0) + 1
    n_code = sum(1 for r in rows.values() if r["decision"]["decision"] == "change_code" and r["id"] in commits)
    n_spec = sum(1 for r in rows.values() if r["decision"]["decision"] == "change_spec" and r["id"] in commits)
    title = f"{prefix} {n_code} code fix(es), {n_spec} spec update(s) from {len(plan)} checked requirements"

    B = ["## Spec drift: specs vs code", "",
         f"Every requirement in {len(specs['specs'])} spec file(s) was split out, mapped to the code that implements it, "
         f"and given a verdict by a calibrated classifier (JEV) from evidence read from the files. "
         f"Each change below was decided by a person, or defaulted to a code fix only when the classifier was sure the code breaks the spec.", "",
         f"**{len(plan)} requirements:** " + ", ".join(f"{n} `{k}`" for k, n in sorted(counts.items())), ""]
    B += ["| spec file | requirements | last changed |", "|---|---|---|"]
    for s in specs["specs"]:
        B.append(f"| `{s['spec']}` | {s['requirements']} | {cell(s['last_commit'], 60)} |")
    if specs.get("truncated"):
        B.append(f"\n{specs['truncated']} requirements were over `max_requirements` and were NOT checked this run.")

    B += ["", "### Requirement → verdict → decision → change", "",
          "| id | requirement | where | verdict | decision (by) | change | re-judged |", "|---|---|---|---|---|---|---|"]
    order = {"change_code": 0, "change_spec": 1, "skip": 2}
    for r in sorted(rows.values(), key=lambda r: (order.get(r["decision"]["decision"], 9), r["id"])):
        d = r["decision"]
        a = acts.get(r["id"], {})
        ch = ", ".join(f"`{s}`" for s in commits.get(r["id"], []))
        if ch and a.get("summary"):
            ch += " " + cell(a["summary"], 120)
        elif d["decision"] in ("change_code", "change_spec"):
            ch = "not made: " + cell(a.get("error") or a.get("status") or "no commit", 100)
        else:
            ch = "—"
        rj = rej.get(r["id"])
        B.append(f"| {r['id']} | {cell(r['text'], 120)} | `{r['where']}` | {r['verdict']} ({r['verdict_band']}) | "
                 f"**{d['decision']}** ({cell(d['by'], 40)}) | {ch} | {(rj or {}).get('verdict') or '—'} |")

    B += ["", "### Why each decision", ""]
    for r in sorted(rows.values(), key=lambda r: r["id"]):
        d = r["decision"]
        B.append(f"- **{r['id']}** {d['decision']} — {cell(d['why'] or r.get('reason') or r['why'], 300)}"
                 + (f" · smallest edit proposed: {cell(r['smallest_edit'], 200)}" if r.get("smallest_edit") else ""))

    s = ver["suite"]
    B += ["", "### Proof", "",
          f"- Suite `{s.get('cmd') or 'none detected'}`: exit {s.get('before')} before, exit {s.get('after')} after.",
          f"- Every changed requirement was re-judged with the same evidence builder and classifier: "
          + (", ".join(f"{r['id']} {r['verdict']} ({r['band']})" for r in ver["rejudge"]) or "none") + ".",
          f"- Commits: " + (", ".join(f"`{c['sha']}` {cell(c['subject'], 80)}" for c in ver["commits"]) or "none") + "."]
    if ver.get("problems"):
        B.append("- **Open problems a person accepted:** " + "; ".join(cell(p, 200) for p in ver["problems"]))
    review = os.environ.get("REVIEW", "").strip()
    if review:
        B += [f"- Adversarial review (a different model): {cell(review, 600)}"]

    conf = [p for p in plan if p["verdict"] == "conforms"]
    B += ["", f"<details><summary>{len(conf)} requirements that conform</summary>", "",
          "| id | requirement | where | code |", "|---|---|---|---|"]
    for p in conf:
        B.append(f"| {p['id']} | {cell(p['text'], 100)} | `{p['where']}` | {cell(', '.join(p['code_refs'][:2]), 80)} |")
    B += ["", "</details>", "", "Generated by the wfnexus `spec-drift-finder` workflow."]
    open(f"{D}/pr-body.md", "w").write("\n".join(B) + "\n")
    open(f"{D}/pr-title.txt", "w").write(title)
    print(title)
    print(f"body: {D}/pr-body.md ({len(B)} lines)")


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"prbody.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
