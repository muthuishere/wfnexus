#!/usr/bin/env python3
"""inventory.py — dead-code-finder's table, run deterministically.

The `dead-code-finder` skill names the finders per language and the exact
candidates.jsonl shape; this script runs exactly those, so the inventory is
reproducible and costs no model call. Every candidate carries the tool line
that produced it. Nothing is edited.

  Go      deadcode -test ./...   (unreachable funcs, whole program incl. tests)
          staticcheck -checks U1000 ./...   (unused funcs/fields/consts/types)
          go mod tidy -diff      (unused module requirements)
  TS/JS   knip --reporter json   (unused files/exports/deps), when node_modules exists

Env: SCOPE. Writes .wfx-code-remover/candidates.jsonl. Exit 78 when there is
nothing to remove, 70 on an error here.
"""
import json
import os
import re
import shutil
import subprocess
import sys

D = ".wfx-code-remover"
SCOPE = os.environ.get("SCOPE") or "."
SKIP_DIRS = {".git", "node_modules", "vendor", "testdata", "dist", "build", ".claude", ".next"}
GOBIN = os.path.expanduser("~/go/bin")


def tool(name, pinned):
    p = shutil.which(name) or (os.path.join(GOBIN, name) if os.path.exists(os.path.join(GOBIN, name)) else None)
    return [p] if p else ["go", "run", pinned]


def roots(marker):
    out = []
    for dp, dn, fn in os.walk(SCOPE):
        dn[:] = [d for d in dn if d not in SKIP_DIRS and not d.startswith(".")]
        if marker in fn:
            out.append(dp)
    if not out:  # scope is inside a module: use the nearest ancestor
        d = os.path.abspath(SCOPE)
        while d.startswith(os.path.abspath(".")):
            if os.path.exists(os.path.join(d, marker)):
                out.append(os.path.relpath(d))
                break
            if d == os.path.abspath("."):
                break
            d = os.path.dirname(d)
    return sorted(out)


def generated(path):
    if path.endswith((".pb.go", "_gen.go", ".gen.go")) or "/vendor/" in path:
        return True
    try:
        with open(path, errors="replace") as f:
            head = f.read(2000)
        return bool(re.search(r"^// Code generated .* DO NOT EDIT\.$", head, re.M))
    except OSError:
        return False


def in_scope(path):
    s = os.path.normpath(SCOPE)
    return s == "." or os.path.normpath(path).startswith(s + os.sep) or os.path.normpath(path) == s


cands, gaps, excluded = {}, [], 0


def add(c):
    global excluded
    if generated(c["file"]):
        excluded += 1
        return
    if not in_scope(c["file"]):
        return
    key = (c["file"], c["line"]) if c["kind"] != "unused-dep" else (c["file"], c["symbol"])
    if key in cands:
        prev = cands[key]
        if c["tool"] not in prev["tool"]:
            prev["tool"] += " + " + c["tool"]
            prev["evidence"] += "\n" + c["evidence"]
        return
    cands[key] = c


def cid(mod, file, symbol):
    pkg = os.path.basename(os.path.dirname(file)) or os.path.basename(mod)
    return f"{pkg}.{symbol}"


def go(mod):
    rel = lambda f: os.path.normpath(os.path.join(mod, f))
    r = subprocess.run(tool("deadcode", "golang.org/x/tools/cmd/deadcode@latest") + ["-test", "./..."],
                       cwd=mod, capture_output=True, text=True, timeout=900)
    if r.returncode and not r.stdout:
        gaps.append(f"{mod}: deadcode failed: {r.stderr.strip()[:300]}")
    for line in r.stdout.splitlines():
        m = re.match(r"(.+?\.go):(\d+):(\d+): unreachable func: (.+)$", line.strip())
        if m:
            f = rel(m[1])
            add({"id": cid(mod, f, m[4]), "kind": "unreachable-func", "file": f, "line": int(m[2]),
                 "symbol": m[4], "name": m[4].split(".")[-1], "tool": "deadcode -test",
                 "evidence": f"{f}:{m[2]}:{m[3]}: unreachable func: {m[4]}", "testOnly": f.endswith("_test.go"),
                 "module": mod})
    r = subprocess.run(tool("staticcheck", "honnef.co/go/tools/cmd/staticcheck@latest") + ["-checks", "U1000", "./..."],
                       cwd=mod, capture_output=True, text=True, timeout=900)
    if r.returncode not in (0, 1) or ("U1000" not in r.stdout and r.returncode == 1 and r.stderr.strip()):
        gaps.append(f"{mod}: staticcheck failed: {(r.stderr or r.stdout).strip()[:300]}")
    for line in r.stdout.splitlines():
        m = re.match(r"(.+?\.go):(\d+):(\d+): (func|field|const|var|type) (.+?) is unused \(U1000\)", line.strip())
        if not m:
            continue
        f, what, sym = rel(m[1]), m[4], m[5]
        sym = re.sub(r"^\(\*?([\w.]+)\)\.", r"\1.", sym)  # (*Engine).x -> Engine.x
        kind = {"func": "unreachable-func", "type": "unused-type"}.get(what, "unused-ident")
        add({"id": cid(mod, f, sym), "kind": kind, "file": f, "line": int(m[2]), "symbol": sym,
             "name": sym.split(".")[-1], "tool": "staticcheck U1000",
             "evidence": f"{f}:{m[2]}:{m[3]}: {what} {m[5]} is unused (U1000)", "testOnly": f.endswith("_test.go"),
             "module": mod})
    r = subprocess.run(["go", "mod", "tidy", "-diff"], cwd=mod, capture_output=True, text=True, timeout=600)
    for line in r.stdout.splitlines():
        m = re.match(r"^-\s+([\w./\-]+) (v\S+)", line)
        if m and not line.startswith("---"):
            f = rel("go.mod")
            add({"id": f"gomod.{m[1]}", "kind": "unused-dep", "file": f, "line": 0, "symbol": m[1],
                 "name": m[1], "tool": "go mod tidy -diff", "evidence": line.strip(), "testOnly": False, "module": mod})


def node(pkg):
    if not os.path.isdir(os.path.join(pkg, "node_modules")):
        gaps.append(f"{pkg}: knip not run — node_modules is not installed in this checkout")
        return
    r = subprocess.run(["npx", "--yes", "knip@5", "--reporter", "json", "--no-exit-code"], cwd=pkg,
                       capture_output=True, text=True, timeout=900)
    try:
        data = json.loads(r.stdout)
    except ValueError:
        gaps.append(f"{pkg}: knip output unreadable: {(r.stderr or r.stdout).strip()[:200]}")
        return
    for f in data.get("files", []):
        p = os.path.normpath(os.path.join(pkg, f))
        add({"id": f"file.{p}", "kind": "unused-file", "file": p, "line": 0, "symbol": p, "name": os.path.basename(p),
             "tool": "knip", "evidence": f"knip: unused file {f}", "testOnly": False, "module": pkg})
    for issue in data.get("issues", []):
        p = os.path.normpath(os.path.join(pkg, issue.get("file", "")))
        for key, kind in (("exports", "unused-export"), ("types", "unused-type"), ("dependencies", "unused-dep"),
                          ("devDependencies", "unused-dep")):
            for it in issue.get(key, []):
                add({"id": f"{os.path.basename(p)}.{it['name']}", "kind": kind, "file": p, "line": it.get("line", 0),
                     "symbol": it["name"], "name": it["name"], "tool": "knip",
                     "evidence": f"knip: unused {key[:-1]} {it['name']} in {issue.get('file')}", "testOnly": False,
                     "module": pkg})


def main():
    mods = roots("go.mod")
    if mods and not shutil.which("go"):
        gaps.append("go is not installed — Go finders not run")
        mods = []
    for m in mods:
        go(m)
    for p in roots("package.json"):
        node(p)
    out = sorted(cands.values(), key=lambda c: (c["file"], c["line"]))
    with open(os.environ.get("CANDIDATES_OUT") or f"{D}/candidates.jsonl", "w") as f:
        for c in out:
            f.write(json.dumps(c) + "\n")
    by_kind, by_tool = {}, {}
    for c in out:
        by_kind[c["kind"]] = by_kind.get(c["kind"], 0) + 1
        by_tool[c["tool"]] = by_tool.get(c["tool"], 0) + 1
    print(f"{len(out)} candidates under {SCOPE} · by kind {by_kind} · by tool {by_tool} · "
          f"{excluded} generated excluded")
    for c in out[:40]:
        print(f"  {c['kind']:16} {c['file']}:{c['line']}  {c['symbol']}  [{c['tool']}]" + ("  (test-only)" if c["testOnly"] else ""))
    for g in gaps:
        print("  GAP:", g)
    sys.exit(0 if out else 78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"inventory.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
