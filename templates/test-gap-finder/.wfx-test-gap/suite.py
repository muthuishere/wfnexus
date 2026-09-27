#!/usr/bin/env python3
"""suite.py — run the repo's own suite the way the style step found it, and COUNT.

Every number in the PR (tests before/after, coverage before/after, which new
test passed) comes from this one ruler, run the same way each time:

    python3 suite.py --out before.json [--no-coverage]

Reads .wfx-test-gap/style.json (the style step's typed answer: framework,
test_command, source_dirs). Per framework it adds only what is needed to see
each test's id and status and the line coverage:

    pytest / unittest   -v (node ids + status), pytest-cov JSON (a private venv
                        with --system-site-packages is made when pytest-cov is
                        missing, so the repo's environment is never modified)
    mocha               --reporter json, wrapped in nyc (or c8) for coverage
    jest / vitest       --json / --reporter=json, their own --coverage
    go-test             go test -json -coverprofile

Writes {ok, totals, tests:{id:status}, coverage:{percent, files:{path:{percent,
missing:[lines]}}}, gaps:[...], tail}. A coverage tool that cannot run is a GAP
in the output, never a silent zero.

Exit 0 green, 1 red, 70 when this script itself broke.
"""
import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys

D = ".wfx-test-gap"


def sh(cmd, env=None, timeout=3600):
    try:
        r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, timeout=timeout,
                           env={**os.environ, "CI": "1", "PYTHONDONTWRITEBYTECODE": "1",
                                "COVERAGE_FILE": os.path.abspath(f"{D}/.coverage"), **(env or {})})
        return r.returncode, r.stdout, r.stderr
    except subprocess.TimeoutExpired:
        return 124, "", f"timed out after {timeout}s"


def tail(s, n=2500):
    s = (s or "").strip()
    return s if len(s) <= n else "…" + s[-n:]


def with_args(cmd, args):
    """Append runner args; npm/yarn/pnpm scripts need them after `--`."""
    first = cmd.strip().split()[0] if cmd.strip() else ""
    if first in ("npm", "yarn", "pnpm") and " -- " not in f" {cmd} ":
        return f"{cmd} -- {args}"
    return f"{cmd} {args}"


# ── python ───────────────────────────────────────────────────────────────────
def py_env(want_cov):
    """PATH with a python that has pytest-cov, without touching the repo's env."""
    if not want_cov:
        return {}, None
    probe = subprocess.run(["python3", "-c", "import pytest_cov"], capture_output=True)
    if probe.returncode == 0:
        return {}, None
    venv = os.path.abspath(f"{D}/.venv")
    if not os.path.exists(f"{venv}/bin/python3"):
        r = subprocess.run(["python3", "-m", "venv", "--system-site-packages", venv], capture_output=True, text=True)
        if r.returncode:
            return {}, "could not make a venv for pytest-cov: " + tail(r.stderr, 300)
        pkgs = ["pytest", "pytest-cov"]
        r = subprocess.run([f"{venv}/bin/python3", "-m", "pip", "install", "-q", *pkgs], capture_output=True, text=True)
        if r.returncode:
            return {}, "pytest-cov could not be installed: " + tail(r.stderr, 300)
        for req in ("requirements.txt", "requirements-dev.txt", "requirements_test.txt"):
            if os.path.exists(req):
                subprocess.run([f"{venv}/bin/python3", "-m", "pip", "install", "-q", "-r", req], capture_output=True)
    return {"PATH": f"{venv}/bin:" + os.environ.get("PATH", ""), "VIRTUAL_ENV": venv}, None


def cd_dir(cmd):
    """`cd scripts && uv run pytest tests` runs from scripts/: ids and relative paths are from there."""
    m = re.match(r"^\s*cd\s+(\S+)\s*&&", cmd or "")
    return m[1] if m else "."


def import_roots(style):
    """Directories that must be on sys.path for `import <package>` to work.

    The root is the first ancestor that is NOT a package: src/pkg/sub -> src.
    Never a package directory itself — putting src/pkg on sys.path lets its
    modules shadow the standard library (a pkg/tokenize.py breaks `import
    inspect` for every tool in the process).
    """
    roots = ["src"] if os.path.isdir("src") else []
    for d in style.get("source_dirs") or []:
        d = os.path.normpath(d.rstrip("/") or ".")
        if os.path.isfile(d):  # a module file in scope: its directory is the package
            d = os.path.dirname(d) or "."
        while d not in ("", ".") and os.path.exists(os.path.join(d, "__init__.py")):
            d = os.path.dirname(d)
        roots.append(d or ".")
    return list(dict.fromkeys(roots))


# a project runner, or an interpreter inside a virtualenv: the project is installed
# there, and extra sys.path entries only shadow it (two citenexus tests broke)
MANAGED = re.compile(r"\b(uv run|poetry run|hatch run|pdm run|tox|nox|rye run)\b|\S*/(\.?venv|env)/bin/python")


def pytest_command(style):
    """The style step's command as a pytest invocation that can import the package.

    pytest runs unittest suites too, so a `python3 -m unittest discover -s tests`
    becomes `python3 -m pytest tests`. Either way the test start dir and the
    source import roots are added to PYTHONPATH (what `discover -s` and an
    editable install would have provided) — harmless when already importable.
    Returns (command, note-or-None).
    """
    cmd = style["test_command"].strip()
    toks = shlex.split(cmd)
    env = [t for t in toks if re.match(r"^[A-Z_][A-Z0-9_]*=", t)]
    start = None
    for flag in ("-s", "--start-directory"):
        if flag in toks and toks.index(flag) + 1 < len(toks):
            start = toks[toks.index(flag) + 1]
    base = cd_dir(cmd)
    # `-s X` is relative to where the command runs; test_dirs are repo-relative
    start_abs = os.path.abspath(os.path.join(base, start)) if start else None
    start = start or next(iter(style.get("test_dirs") or []), "tests")
    start_abs = start_abs or os.path.abspath(start)
    pp = [e[len("PYTHONPATH="):] for e in env if e.startswith("PYTHONPATH=")]
    paths = ":".join(dict.fromkeys((pp[0].split(":") if pp else [])
                                   + [start_abs]
                                   + [os.path.abspath(r) for r in import_roots(style)]))
    env = [e for e in env if not e.startswith("PYTHONPATH=")] + [f"PYTHONPATH={paths}"]
    if MANAGED.search(cmd):
        # a project runner installs the project into its own env: leave its paths alone
        quiet = re.sub(r"(?<=\s)(-q|-qq|--quiet)(?=\s|$)", "", cmd)
        return (quiet, None) if quiet == cmd else (" ".join(quiet.split()), "dropped -q so each test id is listed")
    if "pytest" in cmd:
        m = re.match(r"^(\s*cd\s+\S+\s*&&\s*)?(.*)$", cmd, re.S)
        # tokens, never a re-joined string: `-m "not integration"` must stay one argument
        # -q/--quiet would cancel the -v that lists each test id and its status
        body = [t for t in shlex.split(m[2]) if not re.match(r"^[A-Z_][A-Z0-9_]*=", t)
                and t not in ("-q", "--quiet", "-qq")]
        new = (m[1] or "") + " ".join(shlex.quote(e) for e in env) + " " + " ".join(shlex.quote(t) for t in body)
        return new, f"import roots added: `{new}`"
    new = " ".join(env + ["python3", "-m", "pytest", shlex.quote(start)])
    return new, f"`{cmd}` is not a pytest command; counted with the equivalent `{new}`"


def repo_rel(p, command):
    """coverage.py reports paths relative to where pytest ran; the rest of the run speaks repo paths."""
    for cand in (p, os.path.join(cd_dir(command), p)):
        if os.path.isfile(cand):
            return os.path.relpath(os.path.abspath(cand))
    return p


def run_pytest(style, cov, out_prefix):
    res = {"gaps": []}
    command, note = pytest_command(style)
    if note:
        res["note"] = note
    m = re.search(r"(\S*/(?:\.?venv|env))/bin/python", command)
    if m:
        # calling a venv's python directly skips activation, so the console
        # scripts it installed (a CLI the tests shell out to) are not on PATH
        venv = os.path.abspath(os.path.join(cd_dir(command), m[1]))
        env = {"VIRTUAL_ENV": venv, "PATH": f"{venv}/bin:" + os.environ.get("PATH", "")}
        if cov and subprocess.run([f"{venv}/bin/python", "-c", "import pytest_cov"], capture_output=True).returncode:
            # the run's own worktree venv, never the developer's
            ok = any(subprocess.run(c, capture_output=True).returncode == 0 for c in (
                ["uv", "pip", "install", "-q", "--python", f"{venv}/bin/python", "pytest-cov"],
                [f"{venv}/bin/python", "-m", "pip", "install", "-q", "pytest-cov"]))
            if not ok:
                res["gaps"].append(f"pytest-cov is not in {m[1]} and could not be installed there")
                cov = False
    else:
        env, gap = py_env(cov)
        if gap:
            res["gaps"].append(gap)
            cov = False
    covf = os.path.abspath(f"{out_prefix}.coverage.json")
    args = "-v -p no:cacheprovider -o console_output_style=classic"
    scope = [os.path.abspath(x) for x in style.get("source_dirs") or ["."]]
    if cov:
        # coverage.py measures directories and modules, not file paths: a file in
        # scope is measured through its directory and the totals are recomputed
        # over the in-scope files below
        for s in dict.fromkeys(os.path.dirname(x) if os.path.isfile(x) else x for x in scope):
            args += f" --cov={shlex.quote(s)}"
        args += f" --cov-branch --cov-report=json:{shlex.quote(covf)}"
        if "uv run" in command and "pytest-cov" not in command:
            command = command.replace("uv run", "uv run --with pytest-cov", 1)
    code, out, err = sh(with_args(command, args), env)
    tests = {}
    for m in re.finditer(r"^(\S+::\S+(?:\[[^\]]*\])?)\s+(PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)", out, re.M):
        tests[m[1]] = m[2].lower()
    for m in re.finditer(r"^(PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)\s+(\S+::\S+)", out, re.M):
        tests.setdefault(m[2], m[1].lower())
    # collection errors (a module that does not import) have no node id
    for m in re.finditer(r"^ERROR (\S+\.py)(?: - |$)", out, re.M):
        tests.setdefault(m[1], "error")
    res.update(code=code, tests=tests, tail=tail(out + err))
    if code == 5:
        res["note"] = "no tests collected"
    if cov:
        if os.path.exists(covf):
            c = json.load(open(covf))
            inscope = lambda f: any(f == x or f.startswith(x.rstrip("/") + "/") for x in scope)
            files = {p: f for p, f in c["files"].items() if inscope(os.path.abspath(repo_rel(p, command)))}
            stm = sum(f["summary"].get("num_statements", 0) + f["summary"].get("num_branches", 0) for f in files.values())
            hit = sum(f["summary"].get("covered_lines", 0) + f["summary"].get("covered_branches", 0) for f in files.values())
            res["coverage"] = {
                "percent": round(100.0 * hit / stm, 2) if stm else round(c["totals"]["percent_covered"], 2),
                "files": {repo_rel(p, command): {"percent": round(f["summary"]["percent_covered"], 2),
                                                 "missing": f.get("missing_lines", [])}
                          for p, f in files.items()},
                "tool": "pytest-cov (line+branch, scope only)"}
        else:
            res["gaps"].append("pytest-cov produced no report: " + tail(err, 300))
    return res


# ── node ─────────────────────────────────────────────────────────────────────
def node_deps():
    if os.path.isdir("node_modules"):
        return None
    lock = os.path.exists("package-lock.json")
    code, out, err = sh(("npm ci" if lock else "npm install") + " --no-audit --no-fund --ignore-scripts")
    return None if code == 0 else "dependencies could not be installed: " + tail(out + err, 400)


def node_cov_files(final):
    files, tot_s, tot_hit = {}, 0, 0
    for path, f in final.items():
        missing, n, hit = set(), 0, 0
        for sid, count in f.get("s", {}).items():
            n += 1
            loc = f["statementMap"][sid]["start"]["line"]
            if count:
                hit += 1
            else:
                missing.add(loc)
        rel = os.path.relpath(path)
        files[rel] = {"percent": round(100.0 * hit / n, 2) if n else 100.0, "missing": sorted(missing)}
        tot_s, tot_hit = tot_s + n, tot_hit + hit
    return files, (round(100.0 * tot_hit / tot_s, 2) if tot_s else None)


def run_node(style, cov, out_prefix):
    res = {"gaps": []}
    gap = node_deps()
    if gap:
        return {**res, "gaps": [gap], "code": 1, "tests": {}, "tail": gap}
    fw = style["framework"]
    rep = os.path.abspath(f"{out_prefix}.results.json")
    covdir = os.path.abspath(f"{out_prefix}.cov")
    cmd = style["test_command"]
    if fw == "mocha":
        cmd = with_args(cmd, f"--reporter json --reporter-option output={shlex.quote(rep)}")
    elif fw == "jest":
        cmd = with_args(cmd, f"--json --outputFile={shlex.quote(rep)}"
                             + (f" --coverage --coverageReporters=json --coverageDirectory={shlex.quote(covdir)}" if cov else ""))
    elif fw == "vitest":
        cmd = with_args(cmd, f"--reporter=json --outputFile={shlex.quote(rep)}"
                             + (f" --coverage.enabled --coverage.reporter=json --coverage.reportsDirectory={shlex.quote(covdir)}" if cov else ""))
    if cov and fw == "mocha":
        if os.path.exists("node_modules/.bin/nyc"):
            cmd = (f"npx nyc --check-coverage=false --reporter=json --report-dir={shlex.quote(covdir)} "
                   f"--temp-dir={shlex.quote(covdir + '.tmp')} {cmd}")
        else:
            cmd = f"npx -y c8 --reporter=json --reports-dir={shlex.quote(covdir)} {cmd}"
    code, out, err = sh(cmd)
    tests = {}
    try:
        r = json.load(open(rep))
        if fw == "mocha":
            # mocha allows two tests with one title; number the repeats so none is lost
            status = {}
            for key, st in (("passes", "passed"), ("pending", "skipped"), ("failures", "failed")):
                for t in r.get(key, []):
                    status.setdefault((t.get("file"), t["fullTitle"]), []).append(st)
            for t in r.get("tests", []):
                k = (t.get("file"), t["fullTitle"])
                st = status.get(k, ["passed"]).pop(0) if status.get(k) else "failed"
                name, n = t["fullTitle"], 2
                while name in tests:
                    name, n = f"{t['fullTitle']} #{n}", n + 1
                tests[name] = st
        else:  # jest and vitest share the jest JSON shape
            for f in r.get("testResults", []):
                for t in f.get("assertionResults", []):
                    tests[t.get("fullName") or t.get("title")] = {"passed": "passed", "failed": "failed"}.get(
                        t.get("status"), "skipped")
    except (OSError, ValueError) as e:
        res["gaps"].append(f"the {fw} JSON report could not be read ({e}); counts from the exit code only")
    res.update(code=code, tests=tests, tail=tail(out + err))
    if cov:
        final = os.path.join(covdir, "coverage-final.json")
        if os.path.exists(final):
            files, pct = node_cov_files(json.load(open(final)))
            res["coverage"] = {"percent": pct, "files": files, "tool": "istanbul statements (nyc/c8)"}
        else:
            res["gaps"].append("no coverage-final.json was produced — coverage not measured")
    return res


# ── go ───────────────────────────────────────────────────────────────────────
def run_go(style, cov, out_prefix):
    res = {"gaps": []}
    prof = os.path.abspath(f"{out_prefix}.coverprofile")
    args = "-json" + (f" -coverprofile={shlex.quote(prof)}" if cov else "")
    cmd = style["test_command"]
    cmd = cmd.replace("go test", f"go test {args}", 1) if "go test" in cmd else f"go test {args} ./..."
    code, out, err = sh(cmd)
    tests = {}
    for line in out.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("Test") and ev.get("Action") in ("pass", "fail", "skip"):
            tests[f"{ev['Package']}/{ev['Test']}"] = {"pass": "passed", "fail": "failed", "skip": "skipped"}[ev["Action"]]
    res.update(code=code, tests=tests, tail=tail(err or out))
    if cov and os.path.exists(prof):
        mod = ""
        if os.path.exists("go.mod"):
            m = re.search(r"^module\s+(\S+)", open("go.mod").read(), re.M)
            mod = m[1] if m else ""
        files, tot, hit = {}, 0, 0
        for line in open(prof).read().splitlines()[1:]:
            m = re.match(r"(.+):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)", line)
            if not m:
                continue
            path = m[1][len(mod) + 1:] if mod and m[1].startswith(mod + "/") else m[1]
            f = files.setdefault(path, {"n": 0, "hit": 0, "missing": set()})
            n = int(m[4])
            f["n"] += n
            tot += n
            if int(m[5]):
                f["hit"] += n
                hit += n
            else:
                f["missing"].update(range(int(m[2]), int(m[3]) + 1))
        res["coverage"] = {"percent": round(100.0 * hit / tot, 2) if tot else None, "tool": "go coverprofile statements",
                           "files": {p: {"percent": round(100.0 * f["hit"] / f["n"], 2) if f["n"] else 100.0,
                                         "missing": sorted(f["missing"])} for p, f in files.items()}}
    elif cov:
        res["gaps"].append("go produced no coverprofile")
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--no-coverage", action="store_true")
    a = ap.parse_args()
    style = json.load(open(f"{D}/style.json"))
    fw = style.get("framework")
    prefix = a.out[:-5] if a.out.endswith(".json") else a.out
    if not (style.get("test_command") or "").strip():
        print("the style step returned no test_command", file=sys.stderr)
        sys.exit(70)
    if fw in ("pytest", "unittest"):
        res = run_pytest(style, not a.no_coverage, prefix)
    elif fw in ("mocha", "jest", "vitest"):
        res = run_node(style, not a.no_coverage, prefix)
    elif fw == "go-test":
        res = run_go(style, not a.no_coverage, prefix)
    else:  # any other runner: exit code only, said out loud
        code, out, err = sh(style["test_command"])
        res = {"code": code, "tests": {}, "tail": tail(out + err),
               "gaps": [f"framework {fw!r}: per-test ids and coverage are not measured"]}
    counts = {"passed": 0, "failed": 0, "error": 0, "skipped": 0, "xfail": 0, "xpass": 0}
    for s in res["tests"].values():
        counts[s] = counts.get(s, 0) + 1
    res["totals"] = counts
    res["ok"] = res["code"] == 0 and counts["failed"] == 0 and counts["error"] == 0
    res["framework"] = fw
    res["command"] = style["test_command"]
    if res.get("note"):
        print("  note:", res["note"])
    json.dump(res, open(a.out, "w"), indent=1)
    cov = res.get("coverage", {}).get("percent")
    print(f"suite {'GREEN' if res['ok'] else 'RED'} (exit {res['code']}) — "
          + ", ".join(f"{v} {k}" for k, v in counts.items() if v)
          + (f" · coverage {cov}%" if cov is not None else " · coverage not measured"))
    for g in res.get("gaps", []):
        print("  GAP:", g)
    sys.exit(0 if res["ok"] else 1)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:  # a bug here must not look like a red suite
        import traceback
        traceback.print_exc()
        print(f"suite.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
