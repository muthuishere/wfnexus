#!/usr/bin/env python3
"""validate.py — prove the new tests, with the same ruler as the baseline.

Independent of the writer: it reads the writer's claims (write.json) and checks
each one against the branch and a fresh suite run.

1. SCOPE. Only test files changed; no line of a pre-existing test deleted
   (weakening an existing assertion to go green is the thing this refuses);
   nothing left uncommitted. A violation fails the run.
2. SUITE. The full suite again, with coverage → after.json. Every test that
   passed before still passes. Every new test the writer claims is found in
   the run and passes — or it is a `bug_found` test, kept (expected-failure
   marker where the framework has one) and reported as a FINDING, never deleted.
   A new test that fails with no bug claim stops for a person (exit 20); they
   answer `accept-red F3-S02: why` to record it as a finding.
3. MUTATION. For each passing new test that names a mutation (file, line,
   original text → mutated text), apply it to the source line, run just that
   test, and restore. The test must FAIL on the mutant: `killed`. A test that
   still passes is `survived` — it pins nothing — and is reported, and the
   reviewer sees it.
4. COVERAGE before → after.

Env: BASE (the run's base commit). Writes verify.json. Exit 0 proven, 20 a
person must look, 70 violation or error.
"""
import json
import os
import re
import shlex
import subprocess
import sys

D = ".wfx-test-gap"
BASE = os.environ["BASE"]


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True)


def is_test_path(p, style):
    p2 = p.replace(os.sep, "/")
    for t in style.get("test_dirs") or []:
        t = t.strip("./")
        if t and (p2 == t or p2.startswith(t + "/")):
            return True
    return bool(re.search(r"(^|/)(test_[^/]*\.py|[^/]*_test\.py|conftest\.py|[^/]*\.(test|spec)\.[jt]sx?|[^/]*_test\.go)$", p2))


IMPORT = re.compile(r"^\s*(from\s+\S+\s+import\b|import\b|const\s+.*=\s*require\(|\"[\w./-]+\"$)")


def removed_lines_not_kept(path):
    """Removed lines of an existing test file that are NOT a widened import.

    Adding a name to an existing import line shows as one line removed and one
    added; that is an addition. Anything else removed — an assertion, a test,
    a fixture — is a change to an existing test, which this workflow refuses.
    """
    diff = git("diff", "-U0", f"{BASE}..HEAD", "--", path).stdout.splitlines()
    removed = [l[1:] for l in diff if l.startswith("-") and not l.startswith("---")]
    added = "\n".join(l[1:] for l in diff if l.startswith("+") and not l.startswith("+++"))
    lost = []
    for l in removed:
        if not l.strip():
            continue
        if IMPORT.match(l):
            names = re.findall(r"[A-Za-z_][\w.]*", re.sub(r"^\s*(from|import|const|require)\b", "", l))
            if all(re.search(r"\b" + re.escape(n) + r"\b", added) for n in names if n not in ("import", "from", "as", "require", "const")):
                continue
        lost.append(l)
    return lost


def locate(tid, tests):
    if not tid:
        return None, None
    if tid in tests:
        return tid, tests[tid]
    leaf = re.split(r"::| > ", tid)[-1].strip()
    hits = [t for t in tests if t.endswith(tid) or t.endswith("::" + leaf) or t.endswith("/" + leaf) or t == leaf
            or t.endswith(" " + tid)]
    if len(hits) == 1:
        return hits[0], tests[hits[0]]
    return None, None


def accepted_red():
    txt = open(f"{D}/human.txt").read() if os.path.exists(f"{D}/human.txt") else ""
    out = {}
    for line in txt.splitlines():
        m = re.match(r"^\s*[-*]?\s*accept[- ]red\b[:\s]*(.*)$", line, re.I)
        if m:
            ids = re.findall(r"\bF\d+-[SU]\d+\b", m[1])
            reason = re.split(r"\bF\d+-[SU]\d+\b", m[1])[-1].strip(" :—-,;")
            for i in ids:
                out[i] = reason
    return out


def mutate(m, test_id, style):
    f, line, orig, new = m.get("file"), int(m.get("line") or 0), m.get("original") or "", m.get("mutated") or ""
    if not (f and os.path.isfile(f) and line and orig and orig != new):
        return "not_tried", "no usable mutation (file, line, original, mutated)"
    src = open(f).read().splitlines(keepends=True)
    if line > len(src) or orig not in src[line - 1]:
        return "not_tried", f"{f}:{line} does not contain {orig!r}"
    tmpl = style.get("single_test_command") or ""
    if style.get("framework") in ("pytest", "unittest") and not ("pytest" in tmpl and "{test}" in tmpl):
        sys.path.insert(0, D)
        from suite import pytest_command
        cmd, _ = pytest_command(style)
        # the suite was counted through pytest, so the ids are pytest node ids
        toks = [t for t in shlex.split(cmd)]
        envs = [t for t in toks if re.match(r"^[A-Z_][A-Z0-9_]*=", t)]
        tmpl = " ".join([shlex.quote(e) for e in envs] + ["python3", "-m", "pytest", "-p", "no:cacheprovider", "{test}"])
    if "{test" not in tmpl:
        return "not_tried", "the style step gave no single_test_command with {test}"
    cmd = tmpl.replace("{test_re}", shlex.quote(re.escape(test_id))).replace("{test}", shlex.quote(test_id))
    src2 = list(src)
    src2[line - 1] = src[line - 1].replace(orig, new, 1)
    open(f, "w").write("".join(src2))
    try:
        venv = os.path.abspath(f"{D}/.venv/bin")
        env = {**os.environ, "CI": "1"}
        if os.path.isdir(venv):
            env["PATH"] = venv + ":" + env.get("PATH", "")
        r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, timeout=600, env=env)
        code = r.returncode
    except subprocess.TimeoutExpired:
        code = 124
    finally:
        open(f, "w").write("".join(src))
    if git("diff", "--quiet", "--", f).returncode:
        git("checkout", "--", f)
    return ("killed" if code != 0 else "survived"), f"{f}:{line} `{orig.strip()}` → `{new.strip()}` (test exit {code})"


def main():
    style = json.load(open(f"{D}/style.json"))
    base = json.load(open(f"{D}/baseline.json"))
    write = json.load(open(f"{D}/write.json"))
    triage = json.load(open(f"{D}/triage.json"))
    approved = {r["id"] for r in triage["rows"] if r["verdict"] == "makes_sense"}
    violations, needs_person, findings = [], [], []

    # 1 ── scope
    # tracked edits left uncommitted, or a TEST file written but never committed;
    # untracked build artefacts (__pycache__, .coverage, node_modules) are not the writer's
    dirty = [d for d in git("status", "--porcelain", "--untracked-files=all").stdout.splitlines()
             if ".wfx-test-gap" not in d and (not d.startswith("??") or is_test_path(d[3:], style))
             and "__pycache__" not in d]
    if dirty:
        violations.append("uncommitted changes left on the branch: " + "; ".join(dirty[:8]))
    changed = []
    for l in git("diff", "--numstat", f"{BASE}..HEAD").stdout.splitlines():
        add, dele, path = l.split("\t", 2)
        changed.append(path)
        existed = git("cat-file", "-e", f"{BASE}:{path}").returncode == 0
        if not is_test_path(path, style):
            violations.append(f"{path} is not a test file — this workflow only adds tests (source is never edited)")
        elif existed and dele not in ("0", "-"):
            lost = removed_lines_not_kept(path)
            if lost:
                violations.append(f"{path}: {len(lost)} line(s) of an EXISTING test file deleted or changed — tests are "
                                  f"only added: " + " | ".join(l.strip()[:80] for l in lost[:4]))
    commits = git("log", "--format=%h %s", f"{BASE}..HEAD").stdout.splitlines()
    if not commits:
        violations.append("no commits on the branch")

    # 2 ── the suite, same ruler
    s = subprocess.run([sys.executable, f"{D}/suite.py", "--out", f"{D}/after.json"], capture_output=True, text=True)
    print(s.stdout.rstrip())
    if s.returncode == 70:
        print(s.stderr[-1200:])
        sys.exit(70)
    before = json.load(open(f"{D}/before.json"))
    after = json.load(open(f"{D}/after.json"))
    bt, at = before["tests"], after["tests"]
    known_red = set(base.get("known_red") or [])
    regressions = [t for t, st in bt.items() if st == "passed" and at.get(t) != "passed"]
    for t in regressions:
        violations.append(f"existing test {t} passed before and is {at.get(t, 'MISSING')} now")

    accept = accepted_red()
    rows, claimed = [], set()
    for t in write.get("tests", []):
        sid = t.get("scenario")
        row = {"scenario": sid, "claimed_status": t.get("status"), "test_id": t.get("test_id"),
               "test_file": t.get("test_file"), "why": t.get("why", "")}
        if sid not in approved:
            violations.append(f"{sid}: a test was written for a scenario nobody approved")
        if t.get("status") == "not_written":
            row["result"] = "not_written"
            rows.append(row)
            continue
        tid, st = locate(t.get("test_id"), at)
        if tid is None:
            violations.append(f"{sid}: test {t.get('test_id')!r} was not found in the suite run")
            row["result"] = "missing"
            rows.append(row)
            continue
        claimed.add(tid)
        row.update(test_id=tid, status=st)
        if st == "passed":
            row["result"] = "passes"
            row["mutation"], row["mutation_detail"] = mutate(t.get("mutation") or {}, tid, style)
        elif st in ("xfail",) or (st in ("failed", "error", "skipped") and t.get("status") == "bug_found"):
            row["result"] = "finding"
            findings.append(f"{sid}: {tid} — {t.get('why', '')}")
        elif sid in accept:
            row["result"] = "finding"
            findings.append(f"{sid}: {tid} — accepted as a bug by a person: {accept[sid]}")
        else:
            row["result"] = "unexplained_red"
            needs_person.append(f"{sid}: new test {tid} is {st} and the writer did not say it found a bug")
        rows.append(row)
    approved_unwritten = sorted(approved - {t.get("scenario") for t in write.get("tests", [])})
    new_unclaimed = sorted(t for t in at if t not in bt and t not in claimed)

    cb = before.get("coverage", {}).get("percent")
    ca = after.get("coverage", {}).get("percent")
    files_cov = {}
    for f, v in (after.get("coverage", {}).get("files") or {}).items():
        b = (before.get("coverage", {}).get("files") or {}).get(f, {}).get("percent")
        if b != v.get("percent"):
            files_cov[f] = [b, v.get("percent")]
    out = {"before": before["totals"], "after": after["totals"], "coverage_before": cb, "coverage_after": ca,
           "coverage_tool": after.get("coverage", {}).get("tool"), "coverage_by_file": files_cov,
           "commits": commits, "changed_files": changed, "tests": rows, "findings": findings,
           "violations": violations, "needs_person": needs_person, "known_red": sorted(known_red),
           "approved_unwritten": approved_unwritten, "new_unclaimed_tests": new_unclaimed,
           "mutation": {k: sum(1 for r in rows if r.get("mutation") == k) for k in ("killed", "survived", "not_tried")}}
    json.dump(out, open(f"{D}/verify.json", "w"), indent=1)

    print(f"\nbefore {before['totals']} · after {after['totals']} · coverage {cb}% → {ca}%")
    print(f"commits: {len(commits)} · new tests: {sum(1 for r in rows if r.get('result') == 'passes')} passing, "
          f"{len(findings)} finding(s) · mutation: {out['mutation']}")
    for r in rows:
        print(f"  {r['scenario']}: {r.get('result')} {r.get('test_id') or ''} {r.get('mutation', '')} {r.get('mutation_detail', '')}")
    if approved_unwritten:
        print("  approved but not written:", ", ".join(approved_unwritten))
    for f in findings:
        print("  FINDING:", f)
    if violations:
        print("\nVIOLATIONS:")
        for v in violations:
            print("  -", v)
        sys.exit(70)
    if needs_person:
        print("\nA person must look — reply `accept-red <id>: why` to keep it as a bug finding:")
        for n in needs_person:
            print("  -", n)
        sys.exit(20)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"validate.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
