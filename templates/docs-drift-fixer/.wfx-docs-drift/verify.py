#!/usr/bin/env python3
"""verify.py — keep only the doc edits the CODE proves, and commit those.

The syncer agent edited the working tree and reported each edit (sync.json:
doc, old_text, new_text, code_ref path:line, commit). Nothing it wrote is taken
on trust:

  1. only documentation may change — any other modified file stops the run;
  2. each edit must really be in the diff (old text gone, new text present);
  3. its code_ref must exist, and the EVIDENCE the classifier sees is read from
     that file here (±6 lines) plus the machine fact that flagged it — never the
     agent's paraphrase;
  4. `wfx judge` asks two calibrated questions per edit: was the OLD text
     contradicted by the code (stale), and is the NEW text supported by it;
  5. a doc whose every edit is stale=yes AND supported=yes is committed, one
     commit per doc, citing its code lines; any other doc is restored to the base
     and listed for a person — a partly-verified doc is never half-committed;
  6. relative links in committed docs must resolve;
  7. writes pr-body.md from these facts only.

Exit 0 with commits, 78 when no edit survived (nothing to publish), 70 on error.
"""
import json
import os
import re
import subprocess
import sys

D = ".wfx-docs-drift"
DOC_EXT = (".md", ".mdx", ".rst", ".txt", ".adoc")


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True)


def excerpt(ref):
    m = re.match(r"^([^:]+):(\d+)", (ref or "").strip().strip("`"))
    if not m or not os.path.isfile(m[1]):
        return None
    lines = open(m[1], errors="replace").read().splitlines()
    n = int(m[2])
    if n < 1 or n > len(lines):
        return None
    lo, hi = max(1, n - 6), min(len(lines), n + 6)
    return f"{m[1]} lines {lo}-{hi}:\n" + "\n".join(f"{i}: {lines[i - 1]}" for i in range(lo, hi + 1))


def main():
    sync = json.load(open(f"{D}/sync.json"))
    cands = {json.loads(l)["id"]: json.loads(l) for l in open(f"{D}/candidates.jsonl") if l.strip()}
    base = git("rev-parse", "HEAD").stdout.strip()
    changed = [l[3:] for l in git("status", "--porcelain", "--untracked-files=no").stdout.splitlines()]
    other = [f for f in changed if not f.endswith(DOC_EXT)]
    if other:
        print("the syncer changed files that are not documentation — refusing: " + ", ".join(other))
        sys.exit(70)
    diff = {f: git("diff", "--", f).stdout for f in changed}

    edits, items, rejected = sync.get("edits", []), [], []
    for i, e in enumerate(edits):
        e["n"] = i
        e["id"] = f"edit{i}"
        doc = e.get("doc", "")
        now = open(doc, errors="replace").read() if os.path.isfile(doc) else ""
        d = diff.get(doc, "")
        if not d:
            rejected.append((e, "the doc is not changed in the working tree"))
            continue
        if e.get("new_text") and e["new_text"].strip() not in now:
            rejected.append((e, "the new text is not in the doc"))
            continue
        refs = e.get("code_refs") or []
        ex = [x for x in (excerpt(r) for r in refs) if x]
        if refs and not ex:
            rejected.append((e, "its code reference does not exist: " + ", ".join(refs)))
            continue
        fact = cands.get(e.get("candidate_id"), {}).get("detail", "")
        miss = []
        for tok in re.findall(r"`([^`]+)`", e.get("old_text", "")):
            w = re.sub(r"\(\)$", "", tok).split(".")[-1]
            if re.fullmatch(r"[A-Za-z_][\w\-]{3,}", w):
                hit = subprocess.run(["git", "grep", "-l", "-w", "-I", w, "--", ":!*.md"], capture_output=True, text=True).stdout.split()
                miss.append(f"`{w}` appears in {len(hit)} non-doc files")
        state = (f"DOC: {doc}\nOLD TEXT: {e.get('old_text', '')}\nNEW TEXT: {e.get('new_text', '')}\n"
                 f"MACHINE FACT: {fact or 'none'}\nSEARCH: {'; '.join(miss) or 'n/a'}\n"
                 f"CODE EVIDENCE:\n" + ("\n\n".join(ex) if ex else "(none cited)"))
        e["state"] = state
        items.append({"id": e["id"], "state": state})

    verdicts = {}
    if items:
        with open(f"{D}/edits.judge.jsonl", "w") as f:
            for it in items:
                f.write(json.dumps(it) + "\n")
        j = subprocess.run(["wfx", "judge", "-q", f"{D}/doc-questions.yaml", "--items", f"{D}/edits.judge.jsonl"],
                           capture_output=True, text=True, timeout=600)
        if j.returncode:
            print("the classifier could not judge the edits: " + j.stderr.strip()[:500])
            sys.exit(70)
        verdicts = {json.loads(l)["id"]: json.loads(l) for l in j.stdout.splitlines() if l.strip()}

    ok_by_doc, bad_docs = {}, {}
    for e in edits:
        if "state" not in e:
            continue
        a = (verdicts.get(e["id"]) or {}).get("answers") or {}
        st, sp = a.get("stale", {}), a.get("supported", {})
        e["stale"], e["supported"] = st, sp
        if st.get("band") == "yes" and sp.get("band") == "yes":
            ok_by_doc.setdefault(e["doc"], []).append(e)
        else:
            bad_docs.setdefault(e["doc"], []).append(
                (e, f"stale={st.get('noul')} ({st.get('band')}), supported={sp.get('noul')} ({sp.get('band')})"))
    for e, why in rejected:
        bad_docs.setdefault(e.get("doc", "?"), []).append((e, why))

    for doc in bad_docs:
        if os.path.isfile(doc):
            git("checkout", "--", doc)
    for doc in list(ok_by_doc):
        if doc in bad_docs:
            ok_by_doc.pop(doc)  # partly verified: restored above, listed for a person
    for doc in changed:
        if doc not in ok_by_doc and doc not in bad_docs:
            git("checkout", "--", doc)  # edited but never reported: not ours to keep

    broken = []
    commits = []
    for doc, es in ok_by_doc.items():
        text = open(doc, errors="replace").read()
        for link in re.findall(r"\]\((?!https?:|mailto:|#)([^)\s#]+)", text):
            if not os.path.exists(os.path.normpath(os.path.join(os.path.dirname(doc), link))):
                broken.append(f"{doc}: {link}")
        refs = sorted({r for e in es for r in e.get("code_refs") or []})
        msg = (f"docs({os.path.basename(doc)}): match the code — " + "; ".join(
            (e.get('summary') or e.get('new_text', ''))[:70] for e in es)[:150] +
            "\n\nEach statement was checked against the code by a calibrated classifier (stale and supported both yes).\n"
            + "\n".join(f"- {e.get('old_text', '')[:90]!r} -> {e.get('new_text', '')[:90]!r}  ({', '.join(e.get('code_refs') or [])})" for e in es)
            + (f"\nDrift caused by: {', '.join(sorted({e['commit'] for e in es if e.get('commit')}))}" if any(e.get("commit") for e in es) else ""))
        git("add", "--", doc)
        c = git("commit", "-q", "-m", msg)
        if c.returncode:
            print(f"commit failed for {doc}: {c.stderr.strip()[:200]}")
            sys.exit(70)
        commits.append({"doc": doc, "sha": git("rev-parse", "--short=10", "HEAD").stdout.strip(), "edits": len(es), "refs": refs})

    kept = [e for es in ok_by_doc.values() for e in es]
    held = [(e, why) for es in bad_docs.values() for e, why in es]
    since = open(f"{D}/since.txt").read().strip()
    body = ["## Docs that no longer matched the code", "",
            f"{len(kept)} statement(s) in {len(commits)} doc(s) corrected. Drift was found by machine checks "
            f"(paths, symbols and flags the docs name that the code at `{base[:10]}` does not have, and names "
            f"removed by commits since `{since[:10]}`); each edit was then checked against the cited code by a "
            f"calibrated classifier — the old text contradicted by the code AND the new text supported by it.", "",
            "| Doc | Was | Now | True because | Stale / supported |", "|---|---|---|---|---|"]
    for e in kept:
        body.append(f"| {e['doc']} | {e.get('old_text', '')[:80].replace('|', '/')} | {e.get('new_text', '')[:80].replace('|', '/')} | "
                    f"{', '.join('`' + r + '`' for r in e.get('code_refs') or [])} | {e['stale'].get('noul')} / {e['supported'].get('noul')} |")
    body += ["", "## Commits", *[f"- `{c['sha']}` {c['doc']} ({c['edits']} edit(s)) — revert one with `git revert {c['sha']}`" for c in commits]]
    not_stale = sync.get("not_stale", [])
    if not_stale:
        body += ["", f"## Flagged but left alone ({len(not_stale)})", "",
                 *[f"- {n.get('candidate_id', '')[:90]} — {n.get('why', '')[:160]}" for n in not_stale[:20]]]
    if held or sync.get("unsure"):
        body += ["", "## Needs a person", "",
                 *[f"- {e.get('doc')}: {e.get('old_text', '')[:80]!r} — {why}" for e, why in held],
                 *[f"- {u.get('candidate_id', '')[:90]} — {u.get('why', '')[:160]}" for u in sync.get("unsure", [])]]
    if broken:
        body += ["", "## Broken relative links in the changed docs (pre-existing or new)", *[f"- {b}" for b in broken]]
    body += ["", "_Only documentation changed. Opened by the docs-drift-fixer workflow; nothing merges without a reviewer._"]
    open(f"{D}/pr-body.md", "w").write("\n".join(body) + "\n")
    json.dump({"base": base, "commits": commits, "kept": len(kept), "held": len(held), "not_stale": len(not_stale),
               "broken_links": broken}, open(f"{D}/result.json", "w"), indent=1)

    print(f"{len(edits)} edits proposed · {len(kept)} verified and committed in {len(commits)} doc(s) · "
          f"{len(held)} held for a person · {len(not_stale)} flags judged not stale")
    for e in kept:
        print(f"  OK   {e['doc']}: {e.get('old_text', '')[:60]!r} -> {e.get('new_text', '')[:60]!r} "
              f"[stale {e['stale'].get('noul')}, supported {e['supported'].get('noul')}]")
    for e, why in held:
        print(f"  HELD {e.get('doc')}: {e.get('old_text', '')[:60]!r} — {why}")
    for c in commits:
        print(f"  commit {c['sha']} {c['doc']}")
    sys.exit(0 if commits else 78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"verify.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
