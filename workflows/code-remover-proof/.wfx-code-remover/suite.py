#!/usr/bin/env python3
"""suite.py — build, vet and test every module under a scope, and COUNT.

A cleanup or an upgrade is proven by numbers, not by "tests pass": how many
tests passed before and after, which failed, whether the build and vet were
clean. This script produces those numbers the same way every time, so the
before and the after are measured by the same ruler.

    python3 suite.py --scope apps/api --out before.json

Go modules get build + vet + `go test -json` (counted per test). Node packages
get `npm run build` + `npm test` (exit codes; counts are not portable across
runners, so they are reported as not measured). Python projects get pytest.
A module with no toolchain on this machine is reported as a GAP, never skipped
silently.

Exit 0 when every module is green, 1 when anything is red, 70 on an error in
this script itself.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys

SKIP_DIRS = {".git", "node_modules", "vendor", "testdata", "dist", "build", ".claude", ".next"}


def sh(cmd, cwd, timeout=1800, env=None):
    try:
        r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout,
                           env={**os.environ, **(env or {})}, shell=isinstance(cmd, str))
        return r.returncode, r.stdout, r.stderr
    except subprocess.TimeoutExpired:
        return 124, "", f"timed out after {timeout}s"
    except FileNotFoundError as e:
        return 127, "", str(e)


def tail(s, n=1500):
    s = (s or "").strip()
    return s if len(s) <= n else "…" + s[-n:]


def find_roots(scope, marker):
    """Every directory under scope holding `marker`; else the nearest ancestor that does."""
    out = []
    for dirpath, dirnames, filenames in os.walk(scope):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS and not d.startswith(".")]
        if marker in filenames:
            out.append(dirpath)
    if out:
        return sorted(out)
    d = os.path.abspath(scope)
    top = os.path.abspath(".")
    while d.startswith(top):
        if os.path.exists(os.path.join(d, marker)):
            return [os.path.relpath(d, top)]
        if d == top:
            break
        d = os.path.dirname(d)
    return []


def go_module(mod):
    res = {"kind": "go", "dir": mod}
    code, out, err = sh(["go", "build", "./..."], mod)
    res["build"] = {"ok": code == 0, "tail": tail(out + err)}
    code, out, err = sh(["go", "vet", "./..."], mod)
    res["vet"] = {"ok": code == 0, "tail": tail(out + err)}
    code, out, err = sh(["go", "test", "-json", "./..."], mod, timeout=3600)
    counts = {"pass": 0, "fail": 0, "skip": 0}
    failing, pkg_fail, output = [], [], {}
    for line in out.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        a, t, p = ev.get("Action"), ev.get("Test"), ev.get("Package", "")
        if a == "output" and t:
            output.setdefault((p, t), []).append(ev.get("Output", ""))
        if a in counts and t:
            counts[a] += 1
            if a == "fail":
                failing.append(f"{p}.{t}")
        elif a == "fail" and not t:
            pkg_fail.append(p)
    res["test"] = {"ok": code == 0, **counts, "failing": failing[:50], "failed_packages": pkg_fail[:50],
                   "tail": tail(err) if code else ""}
    if failing:
        first = failing[0]
        p, _, t = first.rpartition(".")
        res["test"]["first_failure_output"] = tail("".join(output.get((p, t), [])), 1200)
    return res


def node_package(pkg):
    res = {"kind": "node", "dir": pkg}
    try:
        scripts = json.load(open(os.path.join(pkg, "package.json"))).get("scripts", {})
    except (OSError, ValueError) as e:
        return {**res, "gap": f"unreadable package.json: {e}"}
    if not shutil.which("npm"):
        return {**res, "gap": "npm is not installed"}
    if not os.path.isdir(os.path.join(pkg, "node_modules")):
        lock = os.path.exists(os.path.join(pkg, "package-lock.json"))
        code, out, err = sh(["npm", "ci" if lock else "install", "--ignore-scripts", "--no-audit", "--no-fund"], pkg)
        if code:
            return {**res, "gap": "dependencies could not be installed: " + tail(out + err, 400)}
    for name in ("build", "test"):
        if name not in scripts:
            res[name] = {"ok": True, "skipped": f"no `{name}` script"}
            continue
        code, out, err = sh(["npm", "run", name], pkg, env={"CI": "1"})
        res[name] = {"ok": code == 0, "tail": tail(out + err)}
    res["test"].update({"pass": None, "fail": None, "skip": None, "note": "counts not measured for npm"})
    res["vet"] = {"ok": True, "skipped": "no vet for node"}
    return res


def py_project(proj):
    res = {"kind": "python", "dir": proj, "build": {"ok": True, "skipped": "no build step"},
           "vet": {"ok": True, "skipped": "no vet step"}}
    code, out, err = sh([sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider"], proj)
    if code == 5:  # no tests collected
        res["test"] = {"ok": True, "pass": 0, "fail": 0, "skip": 0, "note": "no tests collected"}
        return res
    if "No module named pytest" in err:
        return {**res, "gap": "pytest is not installed"}
    import re
    m = {k: int(v) for v, k in re.findall(r"(\d+) (passed|failed|skipped)", out)}
    res["test"] = {"ok": code == 0, "pass": m.get("passed", 0), "fail": m.get("failed", 0),
                   "skip": m.get("skipped", 0), "tail": tail(out + err) if code else ""}
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scope", default=".")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    if not os.path.exists(a.scope):
        print(f"scope {a.scope!r} does not exist in this checkout", file=sys.stderr)
        sys.exit(70)
    modules = []
    for m in find_roots(a.scope, "go.mod"):
        modules.append(go_module(m) if shutil.which("go") else {"kind": "go", "dir": m, "gap": "go is not installed"})
    for p in find_roots(a.scope, "package.json"):
        modules.append(node_package(p))
    for p in find_roots(a.scope, "pyproject.toml"):
        modules.append(py_project(p))
    ok = bool(modules) and all(
        not m.get("gap") and m["build"]["ok"] and m["vet"]["ok"] and m["test"]["ok"] for m in modules)
    totals = {k: sum((m.get("test") or {}).get(k) or 0 for m in modules) for k in ("pass", "fail", "skip")}
    report = {"ok": ok, "scope": a.scope, "modules": modules, "totals": totals,
              "gaps": [f"{m['dir']}: {m['gap']}" for m in modules if m.get("gap")]}
    json.dump(report, open(a.out, "w"), indent=1)
    for m in modules:
        if m.get("gap"):
            print(f"  {m['kind']:6} {m['dir']}: GAP — {m['gap']}")
            continue
        t = m["test"]
        print(f"  {m['kind']:6} {m['dir']}: build {'ok' if m['build']['ok'] else 'RED'} · "
              f"vet {'ok' if m['vet']['ok'] else 'RED'} · tests {t.get('pass')} pass / {t.get('fail')} fail / "
              f"{t.get('skip')} skip")
    if not modules:
        print(f"  no Go, Node or Python module found under {a.scope}")
    print(f"  suite {'GREEN' if ok else 'RED'} — totals {totals}")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:  # a bug here must not look like a red suite
        print(f"suite.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
