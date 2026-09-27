#!/usr/bin/env python3
"""surface.py — enumerate EVERYTHING a test could pin, by code, so nothing is missed.

The scenarios step is an agent, and an agent left to itself looks where it
feels like looking. This script decides what must be looked at: every public
function, method and class; every branch, error path, raise/throw, catch,
early return, loop and null-handling operator inside them; every comparison
against a literal (a boundary); every README code example and every docstring
promise. Each gets an id and a file:line. The coverage report from the
baseline marks which lines no test ever executed.

The analysts must then ACCOUNT for every id (a scenario, `covered` with the
test that pins it, or not-applicable with a reason) — check.py enforces that
by code, and merge.py turns anything still unaccounted into a scenario for a
human. Exhaustive by construction, not by hoping the model was thorough.

Languages: Python (ast), TypeScript/JavaScript (the repo's own `typescript`
compiler via surface_ts.js), Go (surface_go.go via `go run`). Files are then
sharded, balanced by item count, for the per-shard analyst steps.

Env: SHARDS (max shard steps, default 4). Exit 0 = 4 shards used; 11/12/13 =
only 1/2/3 shards needed (the workflow skips the unused shard steps);
78 = nothing to enumerate; 70 = error.
"""
import ast
import json
import os
import re
import subprocess
import sys

D = ".wfx-test-gap"
SHARDS = int(os.environ.get("SHARDS") or 4)
SKIP = {".git", "node_modules", "vendor", "dist", "build", ".venv", "venv", "__pycache__", ".wfx-test-gap",
        "coverage", ".nyc_output", "site-packages"}


def is_test_path(p, style):
    p2 = p.replace(os.sep, "/")
    for t in style.get("test_dirs") or []:
        t = t.strip("./")
        if t and (p2 == t or p2.startswith(t + "/")):
            return True
    b = os.path.basename(p2)
    return bool(re.search(r"(^test_.*\.py$|_test\.py$|\.test\.[jt]sx?$|\.spec\.[jt]sx?$|_test\.go$|^conftest\.py$)", b))


def walk(roots, exts, style):
    out = []
    for root in roots:
        if os.path.isfile(root):
            out.append(root)
            continue
        for dp, dn, fn in os.walk(root):
            dn[:] = sorted(d for d in dn if d not in SKIP and not d.startswith("."))
            for f in sorted(fn):
                p = os.path.relpath(os.path.join(dp, f))
                if f.endswith(exts) and not is_test_path(p, style) and not f.endswith(".d.ts"):
                    out.append(p)
    return sorted(set(out))


# ── python ───────────────────────────────────────────────────────────────────
def py_items(path):
    src = open(path, encoding="utf-8", errors="replace").read()
    try:
        tree = ast.parse(src)
    except SyntaxError as e:
        return [{"kind": "unparsable", "line": e.lineno or 1, "symbol": path, "detail": str(e)}]
    items = []
    seg = lambda n: (ast.get_source_segment(src, n) or "").strip().splitlines()[0][:160] if n else ""

    def public(name):
        return not name.startswith("_") or (name.startswith("__") and name.endswith("__") and name != "__repr__")

    def params(fn):
        a = fn.args
        names = [x.arg for x in a.posonlyargs + a.args + a.kwonlyargs if x.arg not in ("self", "cls")]
        if a.vararg:
            names.append("*" + a.vararg.arg)
        if a.kwarg:
            names.append("**" + a.kwarg.arg)
        return names

    def body(fn, owner):
        for n in ast.walk(fn):
            if n is fn:
                continue
            ln = getattr(n, "lineno", None)
            if isinstance(n, ast.If):
                items.append({"kind": "branch", "line": ln, "probe": n.body[0].lineno, "symbol": owner, "detail": "if " + seg(n.test)})
                if n.orelse and not (len(n.orelse) == 1 and isinstance(n.orelse[0], ast.If)):
                    items.append({"kind": "branch", "line": n.orelse[0].lineno, "probe": n.orelse[0].lineno, "symbol": owner,
                                  "detail": "else of `if " + seg(n.test) + "`"})
            elif isinstance(n, ast.IfExp):
                items.append({"kind": "branch", "line": ln, "symbol": owner, "detail": "conditional expression " + seg(n)})
            elif isinstance(n, ast.Raise):
                items.append({"kind": "raise", "line": ln, "symbol": owner, "detail": seg(n)})
            elif isinstance(n, ast.ExceptHandler):
                items.append({"kind": "except", "line": ln, "probe": n.body[0].lineno, "symbol": owner, "detail": seg(n) or "except"})
            elif isinstance(n, (ast.For, ast.While, ast.AsyncFor)):
                items.append({"kind": "loop", "line": ln, "probe": n.body[0].lineno, "symbol": owner,
                              "detail": seg(n) + " — zero, one and many iterations"})
            elif isinstance(n, (ast.Yield, ast.YieldFrom)):
                items.append({"kind": "generator", "line": ln, "symbol": owner,
                              "detail": "lazy: " + seg(n) + " — laziness, exhaustion, re-iteration"})
            elif isinstance(n, ast.Compare):
                if any(isinstance(c, ast.Constant) for c in [n.left, *n.comparators]):
                    items.append({"kind": "boundary", "line": ln, "symbol": owner,
                                  "detail": seg(n) + " — at, just below and just above the literal; None"})
            elif isinstance(n, ast.Assert):
                items.append({"kind": "raise", "line": ln, "symbol": owner, "detail": seg(n)})
            elif isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id in (
                    "open", "int", "float", "next", "getattr"):
                items.append({"kind": "call-that-can-raise", "line": ln, "symbol": owner, "detail": seg(n)})

    def fn_item(fn, owner_cls=None):
        name = fn.name if not owner_cls else f"{owner_cls}.{fn.name}"
        doc = ast.get_docstring(fn)
        kind = "method" if owner_cls else "function"
        deco = [seg(d) for d in fn.decorator_list]
        first = fn.body[1] if (doc and len(fn.body) > 1) else fn.body[0]
        items.append({"kind": kind, "line": fn.lineno, "probe": first.lineno, "symbol": name,
                      "detail": f"def {fn.name}({', '.join(params(fn))})" + (f" @{','.join(deco)}" if deco else "")
                                + " — every parameter: empty, None, zero/negative, huge, unicode, wrong type"})
        if doc:
            items.append({"kind": "doc_claim", "line": fn.body[0].lineno, "symbol": name, "detail": doc[:300]})
        body(fn, name)

    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and public(node.name):
            fn_item(node)
        elif isinstance(node, ast.ClassDef) and not node.name.startswith("_"):
            items.append({"kind": "class", "line": node.lineno, "symbol": node.name,
                          "detail": f"class {node.name} — construction, equality/repr if defined, chaining"})
            if ast.get_docstring(node):
                items.append({"kind": "doc_claim", "line": node.body[0].lineno, "symbol": node.name,
                              "detail": ast.get_docstring(node)[:300]})
            for m in node.body:
                if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef)) and public(m.name):
                    fn_item(m, node.name)
        elif isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and not t.id.startswith("_") for t in node.targets):
            if isinstance(node.value, (ast.Call, ast.Lambda)):
                items.append({"kind": "public-value", "line": node.lineno, "symbol": seg(node.targets[0]),
                              "detail": seg(node)})
    return items


# ── typescript / javascript and go: helpers in their own language ────────────
def ts_items(files):
    ts = os.path.abspath("node_modules/typescript")
    if not os.path.isdir(ts):
        subprocess.run(["npm", "install", "--no-audit", "--no-fund", "--ignore-scripts"], capture_output=True)
    helper = os.path.join(os.path.dirname(os.path.abspath(__file__)), "surface_ts.js")
    r = subprocess.run(["node", helper, *files], capture_output=True, text=True)
    if r.returncode:
        raise RuntimeError("surface_ts.js failed: " + r.stderr[-800:])
    return json.loads(r.stdout)


def go_items(files):
    helper = os.path.join(os.path.dirname(os.path.abspath(__file__)), "surface_go.go")
    r = subprocess.run(["go", "run", helper, *files], capture_output=True, text=True, env={**os.environ, "GOWORK": "off"})
    if r.returncode:
        raise RuntimeError("surface_go.go failed: " + r.stderr[-800:])
    return json.loads(r.stdout)


# ── specs: every spec scenario and every normative rule is a promise ──────────
NORMATIVE = re.compile(r"\b(SHALL|MUST|SHOULD|REQUIRED|shall|must|should|required|always|never|only|default|at least|at most)\b")


def spec_files():
    """SPEC_PATHS (space-separated globs) when given; else OpenSpec and *spec*.md files."""
    import glob
    pats = (os.environ.get("SPEC_PATHS") or "").split()
    if not pats:
        pats = ["openspec/specs/**/*.md", "specs/**/*.md", "**/*spec*.md", "**/*SPEC*.md"]
    out = []
    for pat in pats:
        for f in glob.glob(pat, recursive=True):
            parts = set(f.split(os.sep))
            if os.path.isfile(f) and f.endswith(".md") and not parts & SKIP and "openspec/changes" not in f:
                out.append(os.path.relpath(f))
    return sorted(set(out))


def spec_items(path):
    """OpenSpec: one item per `### Requirement` (its SHALL line) and per `#### Scenario`
    (its WHEN/THEN/AND bullets). Any other spec doc: one item per rule-bearing line —
    a bullet, a table row, or a sentence with a normative word — outside code fences,
    labelled with its heading so the analyst knows the context."""
    lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
    items, heading, req, fence = [], "", "", False
    openspec = any(l.startswith("### Requirement:") for l in lines)
    i = 0
    while i < len(lines):
        l = lines[i]
        if l.strip().startswith("```"):
            fence = not fence
            i += 1
            continue
        if fence:
            i += 1
            continue
        if l.startswith("#"):
            heading = l.lstrip("#").strip()
        if openspec:
            if l.startswith("### Requirement:"):
                req = heading
                body = next((x.strip() for x in lines[i + 1:i + 6] if NORMATIVE.search(x)), "")
                items.append({"kind": "spec_requirement", "line": i + 1, "symbol": heading,
                              "detail": f"{heading}: {body}"[:400]})
            elif l.startswith("#### Scenario:"):
                steps, j = [], i + 1
                while j < len(lines) and not lines[j].startswith("#"):
                    if lines[j].strip().startswith(("-", "*")):
                        steps.append(lines[j].strip("-* ").replace("**", ""))
                    j += 1
                items.append({"kind": "spec_scenario", "line": i + 1, "symbol": req or heading,
                              "detail": (f"{req} / {heading} — " + "; ".join(steps))[:400]})
        else:
            t = l.strip()
            row = t.startswith("|") and not re.match(r"^\|[\s:|-]+\|$", t)
            header = row and i + 1 < len(lines) and re.match(r"^\|[\s:|-]+\|$", lines[i + 1].strip())
            bullet = bool(re.match(r"^([-*]|\d+\.)\s+\S", t))
            if (row and not header) or bullet or (t and not t.startswith("#") and NORMATIVE.search(t)):
                items.append({"kind": "spec_rule", "line": i + 1, "symbol": heading or path,
                              "detail": f"[{heading}] {t}"[:400]})
        i += 1
    return items


# ── docs: every README example is a promise ──────────────────────────────────
def doc_items():
    out, seen = [], set()
    for readme in ("README.md", "readme.md", "README.rst", "docs/README.md"):
        # a case-insensitive filesystem answers both README.md and readme.md
        if not os.path.exists(readme) or os.path.realpath(readme).lower() in seen:
            continue
        seen.add(os.path.realpath(readme).lower())
        lines = open(readme, encoding="utf-8", errors="replace").read().splitlines()
        i = 0
        while i < len(lines):
            if lines[i].strip().startswith("```"):
                start, block = i + 1, []
                i += 1
                while i < len(lines) and not lines[i].strip().startswith("```"):
                    block.append(lines[i])
                    i += 1
                code = "\n".join(block).strip()
                if len(code.splitlines()) >= 2 and not re.match(r"^(npm|pip|go get|yarn|pnpm|\$)\s", code):
                    out.append({"file": readme, "kind": "doc_example", "line": start, "symbol": readme,
                                "detail": code[:400]})
            i += 1
    return out


def main():
    style = json.load(open(f"{D}/style.json"))
    lang = style.get("language", "")
    roots = [r for r in (style.get("source_dirs") or ["."]) if os.path.exists(r)]
    if not roots:
        print(f"none of the style step's source_dirs exist: {style.get('source_dirs')}", file=sys.stderr)
        sys.exit(70)
    before = {}
    if os.path.exists(f"{D}/before.json"):
        before = json.load(open(f"{D}/before.json")).get("coverage", {}).get("files", {})

    by_file = {}
    if lang == "python":
        for f in walk(roots, (".py",), style):
            by_file[f] = py_items(f)
    elif lang in ("typescript", "javascript"):
        files = walk(roots, (".ts", ".tsx", ".js", ".mjs", ".cjs") if lang == "javascript" else (".ts", ".tsx"), style)
        by_file = ts_items(files) if files else {}
    elif lang == "go":
        files = walk(roots, (".go",), style)
        by_file = go_items(files) if files else {}
    else:
        print(f"language {lang!r} has no enumerator yet — the analysts get the file list only", file=sys.stderr)
        for f in walk(roots, (".sh", ".bash", ".rb", ".java", ".kt", ".rs", ".cs"), style):
            n = sum(1 for _ in open(f, errors="replace"))
            by_file[f] = [{"kind": "file", "line": 1, "symbol": f, "detail": f"{n} lines — read it whole"}]

    for f in spec_files():
        its = spec_items(f)
        if its:
            by_file[f] = its

    docs = doc_items()
    if docs:
        by_file.setdefault(docs[0]["file"], [])
        by_file[docs[0]["file"]].extend(docs)

    items, fidx = [], {}
    for n, (f, its) in enumerate(sorted(by_file.items()), 1):
        cov = before.get(f) or before.get(os.path.abspath(f)) or {}
        missing = set(cov.get("missing") or [])
        fidx[f] = {"n": n, "items": 0, "coverage": cov.get("percent")}
        for k, it in enumerate(sorted(its, key=lambda x: (x["line"] or 0, x["kind"])), 1):
            it = {"id": f"F{n}-{k:03d}", "file": f, **{x: y for x, y in it.items() if x != "file"}}
            if cov:
                # the first line of the body, not the `def`/`if` line (those run at import / always)
                it["line_executed_by_suite"] = (it.pop("probe", it["line"]) not in missing)
            else:
                it.pop("probe", None)
            items.append(it)
            fidx[f]["items"] += 1
    items = [i for i in items if i["kind"] != "unparsable"] + [i for i in items if i["kind"] == "unparsable"]
    if not items:
        print("nothing to enumerate under", roots)
        sys.exit(78)

    # shard: biggest file first onto the lightest shard (balanced by item count)
    loads = [[0, []] for _ in range(SHARDS)]
    for f, meta in sorted(fidx.items(), key=lambda kv: -kv[1]["items"]):
        if not meta["items"]:
            continue
        loads.sort(key=lambda s: s[0])
        loads[0][0] += meta["items"]
        loads[0][1].append(f)
    used = [sorted(s[1]) for s in loads if s[1]]
    shards = {str(i + 1): files for i, files in enumerate(used)}

    with open(f"{D}/surface.jsonl", "w") as fh:
        for it in items:
            fh.write(json.dumps(it) + "\n")
    json.dump({"shards": shards, "files": fidx}, open(f"{D}/shards.json", "w"), indent=1)
    kinds = {}
    for it in items:
        kinds[it["kind"]] = kinds.get(it["kind"], 0) + 1
    print(f"{len(items)} surface items in {len(fidx)} files (specs: {', '.join(spec_files()) or 'none found'}): " + ", ".join(f"{v} {k}" for k, v in sorted(kinds.items())))
    for f, m in sorted(fidx.items()):
        print(f"  F{m['n']:<3} {f}: {m['items']} items · coverage {m['coverage'] if m['coverage'] is not None else 'n/a'}%")
    for s, files in shards.items():
        print(f"  shard {s}: {', '.join(files)}")
    n = len(shards)
    sys.exit(0 if n >= SHARDS else 10 + n)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"surface.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
