#!/usr/bin/env python3
"""verify.py — prove the branch, independent of the agent that changed it.

1. Every commit on the branch is `spec-drift Rnnn: …` for a requirement a person
   (or the confident default) decided to change, and stays in its lane: a
   change_spec commit touches only spec files (or openspec/changes/**), a
   change_code commit touches no spec file. Nothing decided `skip` was touched.
2. The suite, with the same command as the baseline: green before must be green after.
3. Every changed requirement is RE-JUDGED with the same evidence builder and
   classifier as the first pass — the spec text as it now reads, the code as it
   now is, and the commit's diff — and must come out `conforms`.

Writes verify.json. Exit 0 when all hold, 20 when something does not (the step's
needs_input gate shows what; answer `accept-verify` to continue anyway, which
the PR body then states), 70 on error.
"""
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sdlib import D, answer, evidence, git, judge, load_jsonl, requirements  # noqa: E402


def main():
    base = json.load(open(f"{D}/baseline.json"))
    specs = {s["spec"] for s in json.load(open(f"{D}/specs.json"))["specs"]}
    rows = {r["id"]: r for r in json.load(open(f"{D}/decisions.json"))["decisions"]}
    acts = {a["id"]: a for a in load_jsonl(f"{D}/act.jsonl") if a.get("id")}
    reqs = requirements()
    maps = {m["id"]: m for m in load_jsonl(f"{D}/mapping.jsonl")}
    problems = []

    def is_spec(path):
        return path in specs or path.startswith("openspec/") or path.endswith(".md")

    commits = []
    for line in git("log", "--format=%H%x09%s", f"{base['base']}..HEAD").splitlines():
        sha, subj = line.split("\t", 1)
        files = [f for f in git("show", "--name-only", "--format=", sha).splitlines() if f.strip()]
        m = re.match(r"spec-drift (R\d{3})\b", subj)
        rid = m[1] if m else None
        commits.append({"sha": sha[:7], "subject": subj, "id": rid, "files": files})
        if not rid or rid not in rows:
            problems.append(f"commit {sha[:7]} '{subj}' names no decided requirement")
            continue
        dec = (rows[rid].get("decision") or {}).get("decision")
        if dec not in ("change_code", "change_spec"):
            problems.append(f"commit {sha[:7]} changes {rid}, which was decided {dec}")
        if any(f.startswith(".wfx-") for f in files):
            problems.append(f"commit {sha[:7]} commits workflow scratch files")
        if dec == "change_spec" and [f for f in files if not is_spec(f)]:
            problems.append(f"commit {sha[:7]} ({rid}, change_spec) touches non-spec files: {[f for f in files if not is_spec(f)]}")
        if dec == "change_code" and [f for f in files if f in specs]:
            problems.append(f"commit {sha[:7]} ({rid}, change_code) edits a spec: {[f for f in files if f in specs]}")

    by_id = {}
    for c in commits:
        if c["id"]:
            by_id.setdefault(c["id"], []).append(c)
    wanted = [rid for rid, r in rows.items() if (r.get("decision") or {}).get("decision") in ("change_code", "change_spec")]
    for rid in wanted:
        if rid not in by_id:
            st = acts.get(rid, {}).get("status", "no record")
            problems.append(f"{rid} was decided {rows[rid]['decision']['decision']} but has no commit (act: {st})")

    # 2. suite
    suite = {"cmd": base.get("test_cmd"), "before": base.get("suite_exit")}
    if base.get("test_cmd"):
        r = subprocess.run(base["test_cmd"], shell=True, capture_output=True, text=True, timeout=3000)
        suite.update(after=r.returncode, tail=(r.stdout + r.stderr)[-1500:])
        if base.get("suite_exit") == 0 and r.returncode != 0:
            problems.append(f"the suite was green before and is RED after: {suite['tail'][-500:]}")
    else:
        suite["after"] = None

    # 3. re-judge every changed requirement
    items, extra_of = [], {}
    for rid in by_id:
        if rid not in reqs:
            continue
        a = acts.get(rid, {})
        diff = git("show", "--format=%h %s", "--unified=2", *[c["sha"] for c in by_id[rid]])[:5000]
        d = rows[rid]["decision"]
        extra = [f"This change was made to bring the two into agreement (decision {d['decision']}, by {d['by']})."]
        # Behaviour can live outside the repository (a pinned library the code calls). The person's
        # recorded reason, and the recommender's, carry that evidence into the re-judge; they are
        # quoted as what they are, not as code.
        if d.get("why"):
            extra.append(f"The deciding person's recorded reason: \"{d['why'][:600]}\"")
        if rows[rid].get("reason"):
            extra.append(f"The recommender's reason (it read the code): \"{rows[rid]['reason'][:600]}\"")
        extra.append(f"The commit(s), as a diff:\n{diff}")
        mp = dict(maps.get(rid) or {"status": "implemented", "code_refs": [], "test_refs": []})
        # the acting agent's refs first (they point at the code as it is now), de-duplicated
        mp["code_refs"] = list(dict.fromkeys([x for x in (a.get("code_refs") or []) if x] + list(mp.get("code_refs", []))))
        mp["test_refs"] = list(dict.fromkeys([x for x in (a.get("test_refs") or []) if x] + list(mp.get("test_refs", []))))
        # the mapper's prose described the world BEFORE the change; on a re-judge it is stale evidence
        mp["notes"], mp["moved_on"] = "(the mapper's reading predates this change and is not repeated)", ""
        text = a.get("new_text") if rows[rid]["decision"]["decision"] == "change_spec" and a.get("new_text") else None
        items.append({"id": rid, "state": evidence(reqs[rid], mp, reqs, text_override=text,
                                                     where_override=a.get("new_where"), extra=extra)})
        extra_of[rid] = text
    res = judge(items) if items else {}
    rejudge = []
    for it in items:
        v, band, conf = answer(res.get(it["id"]), "verdict")
        rejudge.append({"id": it["id"], "verdict": v, "band": band, "confidence": conf})
        if v != "conforms":
            problems.append(f"{it['id']} re-judged {v} ({band}) after its change — not conforming yet")
    json.dump({"commits": commits, "suite": suite, "rejudge": rejudge, "problems": problems,
               "accepted_with_problems": False}, open(f"{D}/verify.json", "w"), indent=1)

    print(f"{len(commits)} commits · suite `{suite['cmd']}` before {suite['before']} after {suite.get('after')} · "
          f"re-judged {len(rejudge)}: " + ", ".join(f"{r['id']}={r['verdict']}" for r in rejudge))
    if problems:
        raw = open(f"{D}/answers.txt").read() if os.path.exists(f"{D}/answers.txt") else ""
        # only what a person ANSWERED counts, never the question text quoted back
        answers = " ".join(c.split("\nA: ", 1)[1] for c in re.split(r"(?:^|\n)Q: ", raw) if "\nA: " in c)
        print("PROBLEMS:\n  " + "\n  ".join(problems))
        if "accept-verify" in answers:
            v = json.load(open(f"{D}/verify.json"))
            v["accepted_with_problems"] = True
            json.dump(v, open(f"{D}/verify.json", "w"), indent=1)
            print("accepted by a person (accept-verify) — the PR body states these problems")
            sys.exit(0)
        sys.exit(20)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"verify.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
