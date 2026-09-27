#!/usr/bin/env python3
"""verify.py — nothing reaches the reader unless its source says it.

Shared by every leadership template. Reads the agent's draft.json and the
collector's evidence.json, and decides line by line:

 1. CITED      every line names at least one ref, and every ref exists in
               evidence.json (an invented ref is the commonest hallucination).
 2. QUOTED     a line that carries a `quote` must find it, verbatim modulo
               whitespace and case, in the text of the item it cites
               (meeting notes, transcripts, threads).
 3. SUPPORTED  a calibrated JEV classifier (`wfx judge`, question `supported`
               in q.yaml) reads CLAIM + the cited evidence:
               yes -> kept · uncertain -> kept, marked "unconfirmed" · no -> removed.
    Optional questions in q.yaml:
      opinion   (noul) yes -> removed: a judgement about a person, not a fact
      decision  (noul) no  -> removed: not a decision (decision-log)

Then it renders the brief (markdown + typed JSON), including WHAT COULD NOT
BE READ and what verification removed, and prints the success numbers.

Exit 0 = verified brief written. Exit 4 = the classifier could not run, so
nothing was verified (the brief is still written, headed NOT VERIFIED, and
the step's gate fails the run — silence is never "done").
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys

p = argparse.ArgumentParser()
p.add_argument("--evidence", default="evidence.json")
p.add_argument("--draft", default="draft.json")
p.add_argument("--q", default="q.yaml")
p.add_argument("--md", default="brief.md")
p.add_argument("--json", default="brief.json")
p.add_argument("--save-dir", default="")
p.add_argument("--name", default="brief")
p.add_argument("--private", action="store_true")
p.add_argument("--allow-unverified", action="store_true")
a = p.parse_args()

ev = json.load(open(a.evidence))
items = ev["items"]
try:
    draft = json.load(open(a.draft))
except (OSError, json.JSONDecodeError) as ex:
    print(f"draft.json missing or not JSON: {ex}")
    sys.exit(2)

qtext = open(a.q).read()
has_q = {k: bool(re.search(rf"(?m)^{k}:", qtext)) for k in ("supported", "opinion", "decision")}


def norm(s: str) -> str:
    return re.sub(r"\s+", " ", (s or "")).strip().lower()


def source_text(ref: str) -> str:
    it = items.get(ref, {})
    if it.get("path") and os.path.exists(it["path"]):
        return open(it["path"], errors="replace").read()
    return " ".join(str(v) for v in it.values())


def evidence_for(ref: str, cap: int = 1400) -> str:
    it = dict(items.get(ref, {}))
    if it.get("path") and os.path.exists(it["path"]):
        it.pop("head", None)
        it["text"] = open(it["path"], errors="replace").read()[:cap]
    return f"[{ref}] " + json.dumps(it, ensure_ascii=False, default=str)[:cap]


# ── 1 + 2: deterministic checks ───────────────────────────────────────────
lines, removed = [], []
for si, sec in enumerate(draft.get("sections", [])):
    for ii, it in enumerate(sec.get("items", [])):
        lid = f"s{si}i{ii}"
        text = (it.get("text") or "").strip()
        cites = [c.strip().strip("[]") for c in it.get("cites", []) if c and c.strip()]
        row = {"id": lid, "section": si, "item": it, "cites": cites}
        if not text:
            continue
        if not cites:
            removed.append({**row, "reason": "cites no source"})
            continue
        unknown = [c for c in cites if c not in items]
        if unknown:
            removed.append({**row, "reason": f"cites a source that was never read: {', '.join(unknown)[:120]}"})
            continue
        q = it.get("quote")
        if q:
            qref = it.get("quote_ref") or cites[0]
            if norm(q)[:200] not in norm(source_text(qref)):
                removed.append({**row, "reason": f"quote not found in {qref}"})
                continue
        lines.append(row)

# ── 3: the calibrated claim check ─────────────────────────────────────────
verified, verdicts, model = True, {}, ""
if lines:
    with open("claims.jsonl", "w") as f:
        for r in lines:
            it = r["item"]
            state = "CLAIM: " + it["text"]
            if it.get("why"):
                state += "\nREASON GIVEN: " + it["why"]
            if it.get("quote"):
                state += "\nQUOTED: " + it["quote"]
            state += "\nEVIDENCE:\n" + "\n".join(evidence_for(c) for c in r["cites"][:4])
            f.write(json.dumps({"id": r["id"], "state": state}) + "\n")
    pr = subprocess.run(["wfx", "judge", "-q", a.q, "--items", "claims.jsonl"],
                        capture_output=True, text=True, timeout=600)
    for l in pr.stdout.splitlines():
        try:
            v = json.loads(l)
            verdicts[v["id"]] = v
            model = v.get("model", model)
        except (json.JSONDecodeError, KeyError):
            pass
    if pr.returncode != 0 or len(verdicts) < len(lines):
        verified = False
        print(f"claim check incomplete: {len(verdicts)}/{len(lines)} judged — {pr.stderr.strip()[-300:]}")

kept, unconfirmed = [], 0
for r in lines:
    v = verdicts.get(r["id"])
    if not v or "answers" not in v:
        r["verdict"], r["p"] = "unverified", None
        kept.append(r)
        continue
    ans = v["answers"]
    sup = ans.get("supported", {})
    r["verdict"], r["p"] = sup.get("band", "unverified"), sup.get("noul")
    if has_q["opinion"] and ans.get("opinion", {}).get("band") == "yes":
        removed.append({**r, "reason": f"a judgement about a person, not an observable fact (p={ans['opinion'].get('noul')})"})
        continue
    if has_q["decision"] and ans.get("decision", {}).get("band") == "no":
        removed.append({**r, "reason": f"not a decision that was made (p={ans['decision'].get('noul')})"})
        continue
    if r["verdict"] == "no":
        removed.append({**r, "reason": f"the cited source does not support it (p={r['p']})"})
        continue
    if r["verdict"] == "uncertain":
        unconfirmed += 1
    kept.append(r)

# ── render ────────────────────────────────────────────────────────────────
notes, n = [], 0
note_of: dict[str, int] = {}


def cite(ref: str) -> str:
    global n
    if ref not in note_of:
        n += 1
        note_of[ref] = n
        it = items.get(ref, {})
        label = it.get("title") or it.get("subject") or it.get("topic") or it.get("name") or it.get("kind")
        notes.append(f"[{n}] `{ref}` — {str(label)[:90]}" + (f" — {it['url']}" if it.get("url") else ""))
    return f"[{note_of[ref]}]"


src = ev.get("sources", [])
bad = [s for s in src if s["status"] not in ("ok",)]
total_claims = len(kept) + len(removed)
title = draft.get("title") or a.name
md = [f"# {title}", ""]
if a.private:
    md += ["> PRIVATE — prepared for you only. Not shared, not sent.", ""]
md += [f"_Generated {ev['generated_at']} · window {ev.get('window', {}).get('from', '')} → "
       f"{ev.get('window', {}).get('to', '')} · DRAFT: nothing has been sent._", ""]
if not verified and lines:
    md += ["> **NOT VERIFIED** — the claim classifier could not run; treat every line as unchecked.", ""]
if bad:
    md += ["**Could not read:** " + "; ".join(f"{s['name']} ({s.get('reason','')[:80]}) → `{s.get('fix','')}`"
                                             for s in bad), ""]
out_sections = []
for si, sec in enumerate(draft.get("sections", [])):
    rows = [r for r in kept if r["section"] == si]
    out_sections.append({"id": sec.get("id", f"s{si}"), "heading": sec.get("heading", ""), "items": []})
    md.append(f"## {sec.get('heading','')}")
    if not rows:
        md.append(f"- {sec.get('empty') or 'Nothing here.'}")
    for r in sorted(rows, key=lambda r: r["item"].get("rank", 99)):
        it = r["item"]
        mark = " _(unconfirmed)_" if r["verdict"] in ("uncertain", "unverified") else ""
        why = f" — {it['why']}" if it.get("why") else ""
        md.append(f"- {it['text']}{why}{mark} {' '.join(cite(c) for c in r['cites'])}")
        out_sections[-1]["items"].append({
            **{k: v for k, v in it.items() if k not in ("cites",)},
            "cites": [{"ref": c, "url": items[c].get("url", "")} for c in r["cites"]],
            "verdict": r["verdict"], "p_supported": r["p"]})
    md.append("")
for extra in draft.get("appendix", []) or []:
    md += [f"## {extra.get('heading','')}", extra.get("body", ""), ""]

md += ["## Sources", "| source | status | items |", "|---|---|---|"]
for s in src:
    md.append(f"| {s['name']} | {s['status']}{(' — ' + s['fix']) if s.get('fix') else ''} | {s['count']} |")
md += ["", "## Citations", *notes, ""]
if removed:
    md += [f"## Removed by verification ({len(removed)})"]
    for r in removed:
        md.append(f"- ~~{r['item'].get('text','')[:160]}~~ — {r['reason']}")
    md.append("")
stats = {
    "claims_drafted": total_claims, "claims_kept": len(kept), "claims_unconfirmed": unconfirmed,
    "claims_removed": len(removed),
    "supported_pct": round(100 * (len(kept) - unconfirmed) / total_claims, 1) if total_claims else None,
    "sources_ok": len(src) - len(bad), "sources_total": len(src),
    "unreadable_sources": [s["name"] for s in bad], "verified": verified or not lines,
    "classifier": model or None, "citations": len(notes),
}
md += [f"_Verification: {total_claims} claims drafted · {len(kept) - unconfirmed} supported · "
       f"{unconfirmed} unconfirmed · {len(removed)} removed · classifier {model or 'n/a'} · "
       f"sources {stats['sources_ok']}/{stats['sources_total']} readable._"]

brief = {"title": title, "generated_at": ev["generated_at"], "window": ev.get("window"),
         "draft_only": True, "sections": out_sections, "sources": src,
         "removed": [{"text": r["item"].get("text"), "reason": r["reason"]} for r in removed],
         "stats": stats, "private": a.private}
open(a.md, "w").write("\n".join(md) + "\n")
json.dump(brief, open(a.json, "w"), indent=1, default=str)

saved = ""
if a.save_dir:
    d = os.path.expanduser(a.save_dir)
    os.makedirs(d, mode=0o700, exist_ok=True)
    stamp = dt.datetime.now().strftime("%Y-%m-%d-%H%M")
    for ext, path in (("md", a.md), ("json", a.json)):
        dst = os.path.join(d, f"{a.name}-{stamp}.{ext}")
        with open(dst, "w") as f:
            f.write(open(path).read())
        os.chmod(dst, 0o600)
        saved = dst if ext == "md" else saved
print(json.dumps({"stats": stats, "saved": saved}))
print("\n".join(md)[:3000])
sys.exit(0 if (verified or not lines or a.allow_unverified) else 4)
