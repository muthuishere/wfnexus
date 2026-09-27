#!/usr/bin/env python3
"""checks.py — usage-prover's independent checks, run by a machine, per candidate.

usage-prover says "no reference found from fewer than three different checks
is not proof". An agent that CLAIMS it ran three checks is not proof either, so
the checks themselves run here, deterministically, and the agent's job in the
next step is to READ their hits adversarially — not to be trusted to run them.

Per candidate:
  1. literal   git grep -n -w <name> over EVERY tracked file (code, docs, skills,
               templates, config), definition line excluded; split into code hits
               and text hits
  2. call      git grep for call/selector shapes: `name(` and `.name`
  3. indirect  interface-satisfying method names (String, Error, MarshalJSON…),
               an interface in the module declaring the same method, reflection
               (MethodByName/"name"), //go:linkname, a //go:build constraint on
               the file (another build context)
  4. history   git log -S'name(' — when did the last caller go, and in which commit
  5. graph     ctx-optimize callers, when a .ctxoptimize store exists

Writes .wfx-code-remover/checks.jsonl.
"""
import json
import os
import re
import shutil
import subprocess
import sys

D = ".wfx-code-remover"
CODE_EXT = (".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py", ".rs", ".java", ".kt", ".cs", ".rb", ".swift")
STD_IFACE = {"String", "Error", "GoString", "Format", "MarshalJSON", "UnmarshalJSON", "MarshalText",
             "UnmarshalText", "MarshalYAML", "UnmarshalYAML", "Scan", "Value", "ServeHTTP", "Len", "Less",
             "Swap", "Read", "Write", "Close", "Unwrap", "Is", "As", "Seek", "ReadFrom", "WriteTo"}


def run(*a, cwd=None):
    r = subprocess.run(list(a), capture_output=True, text=True, cwd=cwd)
    return r.stdout


def grep(pattern, *flags):
    out = run("git", "grep", "-n", "-I", *flags, "-e", pattern, "--", ".", ":(exclude).wfx-*")
    return [l for l in out.splitlines() if l]


def main():
    cands = [json.loads(l) for l in open(f"{D}/candidates.jsonl") if l.strip()]
    has_graph = os.path.isdir(".ctxoptimize") and shutil.which("ctx-optimize")
    out = []
    for c in cands:
        name, deff = c["name"], f"{c['file']}:{c['line']}:"
        checks = []
        # 1. literal, whole repo
        def own_doc(h):  # the candidate's own doc comment, just above its definition
            f, ln, txt = (h.split(":", 2) + ["", ""])[:3]
            return f == c["file"] and ln.isdigit() and c["line"] - 25 <= int(ln) < c["line"] \
                and txt.strip().startswith(("//", "#", "*", "/*"))
        hits = [h for h in grep(name, "-w") if not h.startswith(deff) and not own_doc(h)]
        code = [h for h in hits if h.split(":", 1)[0].endswith(CODE_EXT)]
        text = [h for h in hits if not h.split(":", 1)[0].endswith(CODE_EXT)]
        checks.append({"check": "literal", "cmd": f"git grep -n -w {name}", "code_hits": len(code),
                       "text_hits": len(text), "empty": not hits})
        # 2. call / selector shapes (drop comment-only lines)
        esc = re.escape(name)
        calls = [h for h in grep(rf"(\b{esc}\s*\(|\.{esc}\b)", "-P")
                 if not h.startswith(deff) and h.split(":", 1)[0].endswith(CODE_EXT)
                 and not re.match(r"^[^:]+:\d+:\s*(//|#|\*)", h)]
        checks.append({"check": "call-shape", "cmd": f"git grep -E '\\b{name}\\s*\\(|\\.{name}\\b'",
                       "hits": len(calls), "empty": not calls})
        # 3. indirect use
        ind = []
        if name in STD_IFACE:
            ind.append(f"{name} satisfies a standard-library interface (called by fmt/json/sql/io without a literal call)")
        if c["file"].endswith(".go") and "." in c["symbol"]:
            # an interface method line: indented `Name(` with no `func` keyword
            decl = [h for h in run("git", "grep", "-n", "-E", rf"^\s+{esc}\(", "--", "*.go").splitlines()
                    if not h.startswith(deff)]
            if decl:
                ind.append(f"a method named {name} is declared in an interface: {decl[0]}")
        refl = grep(rf"(MethodByName\(\"{esc}\"|go:linkname .*{esc}|\"{esc}\")", "-P")
        refl = [h for h in refl if h.split(":", 1)[0].endswith(CODE_EXT)]
        if refl:
            ind.append(f"named by string/reflection/linkname: {refl[0]}")
        try:
            head = open(c["file"], errors="replace").read(1500)
            bt = re.search(r"^//go:build (.+)$", head, re.M)
            if bt:
                ind.append(f"file has a build constraint ({bt[1]}) — another build context may reach it")
        except OSError:
            pass
        checks.append({"check": "indirect", "found": ind, "empty": not ind})
        # 4. history
        hist = run("git", "log", "-S", f"{name}(", "--format=%h %ad %s", "--date=short", "-n", "6")
        hist = [h for h in hist.splitlines() if h]
        checks.append({"check": "history", "cmd": f"git log -S'{name}('", "commits": hist,
                       "empty": True})  # history explains; it never saves code on its own
        # 5. graph
        if has_graph:
            g = run("ctx-optimize", "affected", c["symbol"])
            callers = [l.strip() for l in g.splitlines() if l.strip().startswith("d1 ") and "via calls on" in l]
            checks.append({"check": "graph", "cmd": f"ctx-optimize affected {c['symbol']}",
                           "callers": callers[:10], "empty": not callers})
        independent = [k for k in checks if k["check"] != "history"]
        out.append({
            "id": c["id"], "symbol": c["symbol"], "file": c["file"], "line": c["line"],
            "checks": checks, "checks_run": len(checks),
            "checks_empty": sum(1 for k in independent if k["empty"]),
            "all_independent_empty": all(k["empty"] for k in independent),
            "code_hits": code[:15], "text_hits": text[:15], "call_hits": calls[:15],
            "indirect": ind, "history": hist,
        })
    with open(f"{D}/checks.jsonl", "w") as f:
        for o in out:
            f.write(json.dumps(o) + "\n")
    print(f"{len(out)} candidates checked ({'with' if has_graph else 'without'} a code graph)")
    print(f"{'candidate':42} checks  literal(code/text)  calls  indirect")
    for o in out:
        lit = o["checks"][0]
        print(f"{o['symbol'][:42]:42} {o['checks_empty']}/{o['checks_run'] - 1} empty  "
              f"{lit['code_hits']:>4}/{lit['text_hits']:<4}          {o['checks'][1]['hits']:>4}  "
              f"{'; '.join(o['indirect'])[:60] or '-'}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"checks.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
