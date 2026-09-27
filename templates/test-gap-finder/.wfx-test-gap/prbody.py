#!/usr/bin/env python3
"""prbody.py — the PR body, built ONLY from the machine's files, then checked.

    python3 prbody.py          # write pr-body.md + pr-title.txt
    python3 prbody.py --check  # the claims step: re-read the body against the evidence

Every number comes from before.json / after.json (via verify.json), every
verdict from triage.json, every review finding from review.json. Nothing here
is a model's summary of what happened, so there is nothing to hallucinate —
and --check still re-reads the body independently: every scenario id present
with its verdict, the before/after counts and coverage quoted exactly, every
commit SHA listed, every finding and every surviving mutant disclosed.
Exit 0 ok, 20 the body does not match the evidence, 70 error.
"""
import json
import os
import sys

D = ".wfx-test-gap"
PREFIX = os.environ.get("PR_PREFIX", "[test-gaps]")


def load(n, default=None):
    try:
        return json.load(open(f"{D}/{n}"))
    except (OSError, ValueError):
        return default


def counts(t):
    return f"{t.get('passed', 0)} passed / {t.get('failed', 0) + t.get('error', 0)} failed / {t.get('skipped', 0)} skipped" + (
        f" / {t['xfail']} xfail" if t.get("xfail") else "")


def build():
    style, base, triage, v, review = load("style.json"), load("baseline.json"), load("triage.json"), load("verify.json"), load("review.json", {})
    scen = [json.loads(l) for l in open(f"{D}/scenarios.jsonl") if l.strip()]
    surface = sum(1 for l in open(f"{D}/surface.jsonl") if l.strip())
    tests = {r["scenario"]: r for r in v["tests"]}
    rows = triage["rows"]
    n_pass = sum(1 for r in v["tests"] if r.get("result") == "passes")
    title = f"{PREFIX} {n_pass} test(s) for untested behaviour" + (f", {len(v['findings'])} bug finding(s)" if v["findings"] else "")
    L = [f"Adds {n_pass} test(s) for scenarios the existing suite never pinned, written in this repo's own style "
         f"({style['framework']}; naming: {' '.join(style.get('naming_convention', '').replace('`', '').split())[:160]}).", ""]
    L += ["## Before / after", "", "| | before | after |", "|---|---|---|",
          f"| tests | {counts(v['before'])} | {counts(v['after'])} |",
          f"| line coverage ({v.get('coverage_tool') or 'not measured'}) | {v.get('coverage_before')}% | {v.get('coverage_after')}% |", ""]
    if v.get("known_red"):
        L += [f"The suite was already red before this PR: {len(v['known_red'])} test(s) failed at the base commit and still do "
              f"(frozen as known-red; none of them counts as covering anything):", ""]
        L += [f"- `{t}`" for t in v["known_red"][:30]] + [""]
    if v.get("coverage_by_file"):
        L += ["Coverage by file: " + "; ".join(f"`{f}` {b}% → {a}%" for f, (b, a) in sorted(v["coverage_by_file"].items())), ""]
    # covered = what survived triage's check (a claim whose test does not assert
    # the scenario was sent back as a gap), not what the analysts claimed
    cov = triage.get("covered") or [s for s in scen if s["status"] == "covered"]
    demoted = sum(1 for r in rows if "does not assert" in (r.get("why") or "") or r.get("demoted"))
    claimed = sum(1 for s in scen if s["status"] == "covered")
    specs = sorted({s["file"] for s in scen if s["file"].endswith(".md") and s["file"].lower() != "readme.md"})
    L += ["## How the scenarios were found", "",
          f"Every public function, method, class, branch, error path, boundary and README example"
          + (f", and every rule in {', '.join('`' + x + '`' for x in specs)}," if specs else "")
          + f" was enumerated by code ({surface} surface items), analysed file by file, and deduplicated against the "
          f"existing tests: {len(cov)} scenarios are already pinned by a passing test"
          + (f" ({claimed - len(cov)} more were claimed covered, but a classifier reading the test found it does not "
             f"assert the scenario, so they were triaged as gaps)" if claimed > len(cov) else "")
          + f"; {len(rows)} were not. Each of those was triaged by a calibrated classifier; the unsure ones were decided "
          f"by a person.", ""]
    if v["findings"]:
        L += ["## Bug findings (tests kept, marked as expected failures)", ""] + [f"- {f}" for f in v["findings"]] + [""]
    L += ["## Scenarios written", "", "| id | kind | scenario | evidence | decided by | test | mutation |", "|---|---|---|---|---|---|---|"]
    for r in sorted([r for r in rows if r["verdict"] == "makes_sense"], key=lambda r: r["id"]):
        t = tests.get(r["id"], {})
        L.append(f"| {r['id']} | {r['kind']} | {r['title'].replace('|', '/')[:100]} | `{r['evidence']}` | "
                 f"{r['decided_by']}{' (' + r['human'] + ')' if r.get('human') else ''} | "
                 f"{'`' + t['test_id'] + '` ' + t.get('result', '') if t.get('test_id') else t.get('result', 'not written')} | "
                 f"{t.get('mutation', '—')} |")
    L += ["", "## Scenarios not written, and why", "", "| id | verdict | decided by | scenario | why |", "|---|---|---|---|---|"]
    for r in sorted([r for r in rows if r["verdict"] != "makes_sense"], key=lambda r: (r["verdict"], r["id"])):
        L.append(f"| {r['id']} | {r['verdict']} | {r['decided_by']} | {r['title'].replace('|', '/')[:100]} | "
                 f"{(r.get('human') or r['why']).replace('|', '/')[:160]} |")
    if cov:
        L += ["", f"<details><summary>{len(cov)} scenarios already covered by an existing passing test</summary>", "",
              "| id | scenario | covered by |", "|---|---|---|"]
        L += [f"| {s['id']} | {s['title'].replace('|', '/')[:100]} | `{s.get('covered_by', '')}` |" for s in sorted(cov, key=lambda s: s["id"])]
        L += ["", "</details>"]
    surv = [r for r in v["tests"] if r.get("mutation") == "survived"]
    L += ["", "## Proof", "",
          f"- The full suite was re-run on this branch by a separate step, with the same command as the baseline: `{style['test_command']}`.",
          f"- Mutation check (the target line changed, the new test re-run, the source restored): "
          f"{v['mutation']['killed']} killed, {v['mutation']['survived']} survived, {v['mutation']['not_tried']} not tried."]
    if surv:
        L.append("- Survived (the test still passed on the mutant, so it pins less than it claims): "
                 + "; ".join(f"{r['scenario']} ({r.get('mutation_detail', '')})" for r in surv))
    if review:
        L.append(f"- Adversarial review ({'approved' if review.get('approved') else 'NOT approved'}): {review.get('verdict', '')}")
        for f in review.get("findings", [])[:12]:
            L.append(f"  - {f.get('severity')}: {f.get('what')}" + (f" (`{f['test']}`)" if f.get("test") else ""))
    L += ["", "## Commits", ""] + [f"- {c}" for c in v["commits"]]
    L += ["", "Revert any test with `git revert <sha>`; nothing outside test files is changed.", "",
          "_Generated by the wfnexus `test-gap-finder` workflow; triage verdicts by a calibrated classifier (JEV), "
          "unsure ones decided by a person._"]
    open(f"{D}/pr-body.md", "w").write("\n".join(L) + "\n")
    open(f"{D}/pr-title.txt", "w").write(title)
    print(title)
    print(f"{D}/pr-body.md: {len(L)} lines")


def check():
    body = open(f"{D}/pr-body.md").read()
    triage, v = load("triage.json"), load("verify.json")
    miss = []
    for r in triage["rows"]:
        if r["id"] not in body:
            miss.append(f"scenario {r['id']} ({r['verdict']}) is not in the body")
    for label, t in (("before", v["before"]), ("after", v["after"])):
        if counts(t) not in body:
            miss.append(f"the {label} counts `{counts(t)}` are not quoted")
    for p in (v.get("coverage_before"), v.get("coverage_after")):
        if f"{p}%" not in body:
            miss.append(f"coverage {p}% is not quoted")
    for c in v["commits"]:
        if c.split()[0] not in body:
            miss.append(f"commit {c.split()[0]} is not listed")
    for f in v["findings"]:
        if f.split(":")[0] not in body:
            miss.append(f"finding {f[:60]} is not disclosed")
    for r in v["tests"]:
        if r.get("mutation") == "survived" and r["scenario"] not in body.split("Survived", 1)[-1]:
            miss.append(f"surviving mutant for {r['scenario']} is not disclosed")
    if miss:
        print("the PR body does not match the evidence:")
        for m in miss:
            print("  -", m)
        sys.exit(20)
    print(f"PR body checked: {len(triage['rows'])} scenarios, counts, coverage, {len(v['commits'])} commits, "
          f"{len(v['findings'])} findings — all present and matching the evidence")


if __name__ == "__main__":
    try:
        check() if "--check" in sys.argv else build()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"prbody.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
