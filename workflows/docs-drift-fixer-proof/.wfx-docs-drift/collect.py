#!/usr/bin/env python3
"""collect.py — find doc statements the code (or recent commits) contradicts. No model.

Rules before models: every candidate here is a FACT a machine can re-check —

  moved-path       a doc names a file the recent commits renamed or deleted
                   (git diff -M --name-status SINCE..HEAD)
  removed-symbol   a doc names a function/type/const that a recent commit deleted
                   and that no code defines any more
  missing-path     a doc names a repo path (backticks or a relative link) that
                   does not exist at HEAD
  missing-symbol   a doc names a code identifier (`Pkg.Func`, `func()`, `snake_case`)
                   that appears in no code file at HEAD
  missing-flag     a doc names a `--flag` that appears in no code file

History is left alone: ADRs, changelogs, proposals, spikes and research notes
are dated records of what was true THEN, and "fixing" them rewrites history.

Env: SINCE (a commit; empty = LOOKBACK_DAYS), LOOKBACK_DAYS, DOCS (comma globs),
EXCLUDE (comma globs), MAX_FINDINGS, BRANCH. Writes .wfx-docs-drift/candidates.jsonl
and since.txt. Exit 78 when nothing drifted, 70 on error.
"""
import fnmatch
import json
import os
import re
import subprocess
import sys

D = ".wfx-docs-drift"
DOCS = [g.strip() for g in (os.environ.get("DOCS") or "README.md,**/README.md,docs/**/*.md,**/SKILL.md").split(",") if g.strip()]
EXCLUDE = [g.strip() for g in (os.environ.get("EXCLUDE") or
           "docs/adr/**,**/CHANGELOG*,openspec/**,docs/spikes/**,docs/research/**,.claude/**,.codex/**,.opencode/**,**/node_modules/**,.wfx-*/**").split(",") if g.strip()]
MAX = int(os.environ.get("MAX_FINDINGS") or 25)
CODE_EXT = (".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".py", ".rs", ".java", ".kt", ".cs", ".rb", ".sh",
            ".yaml", ".yml", ".json", ".toml", ".sql")
DEF_RE = re.compile(r"^-\s*(?:func\s+(?:\([^)]*\)\s*)?|def\s+|class\s+|function\s+|export\s+(?:async\s+)?(?:function|const|class|type|interface)\s+|type\s+|const\s+)([A-Za-z_][A-Za-z0-9_]{3,})")
COMMON = {"true", "false", "null", "nil", "none", "string", "error", "main", "json", "yaml", "http", "https",
          "README", "TODO", "id", "name", "type", "value", "input", "output", "steps", "run", "prompt", "skills",
          "tools", "needs", "when", "gates", "schedule", "cron", "default", "description", "title", "summary"}


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout


def match(path, globs):
    return any(fnmatch.fnmatch(path, g) or (g.startswith("**/") and fnmatch.fnmatch(path, g[3:])) for g in globs)


def code_has(word, fixed=False):
    args = ["git", "grep", "-q", "-I", "-w" if not fixed else "-F", "-e", word, "--"] + [f"*{e}" for e in CODE_EXT]
    return subprocess.run(args, capture_output=True).returncode == 0


def main():
    branch = os.environ.get("BRANCH")
    if branch and subprocess.run(["git", "checkout", "-b", branch], capture_output=True).returncode:
        subprocess.run(["git", "checkout", branch], capture_output=True)
    head = git("rev-parse", "HEAD").strip()
    since = (os.environ.get("SINCE") or "").strip()
    if not re.fullmatch(r"[0-9a-f]{7,40}", since) or subprocess.run(
            ["git", "merge-base", "--is-ancestor", since, head], capture_output=True).returncode:
        days = os.environ.get("LOOKBACK_DAYS") or "14"
        since = git("rev-list", "-1", f"--before={days} days ago", "HEAD").strip() or git("rev-list", "--max-parents=0", "HEAD").split()[0]
    open(f"{D}/since.txt", "w").write(since)
    commits = git("log", "--format=%h %s", f"{since}..HEAD").splitlines()

    docs = [p for p in git("ls-files", "*.md").splitlines() if match(p, DOCS) and not match(p, EXCLUDE)]
    tracked = git("ls-files").splitlines()

    def path_known(t):
        # a path written relative to a module or package ("cmd/wfx/main.go" inside apps/api)
        # still resolves; only a path nothing in the repo ends with is missing
        t = t.strip("./").rstrip("/")
        return any(p == t or p.endswith("/" + t) or p.startswith(t + "/") or ("/" + t + "/") in ("/" + p) for p in tracked)
    cands, seen = [], set()

    def add(kind, doc, line, token, detail, commit="", prio=5):
        key = (doc, token)
        if key in seen:
            return
        seen.add(key)
        cands.append({"id": f"{kind}:{doc}:{line}:{token}"[:180], "kind": kind, "doc": doc, "line": line,
                      "token": token, "detail": detail, "commit": commit, "prio": prio})

    # recent renames / deletions and removed definitions
    moved = {}
    for l in git("diff", "-M", "--name-status", f"{since}..HEAD").splitlines():
        parts = l.split("\t")
        if parts[0].startswith("R") and len(parts) == 3:
            moved[parts[1]] = parts[2]
        elif parts[0] == "D" and len(parts) == 2:
            moved[parts[1]] = None
    removed = {}
    for sha in git("rev-list", f"{since}..HEAD").split():
        patch = git("show", "--format=", "-U0", sha, "--", *[f"*{e}" for e in CODE_EXT[:9]])
        for l in patch.splitlines():
            m = DEF_RE.match(l)
            if m and m[1] not in COMMON:
                removed.setdefault(m[1], sha[:10])
    removed = {n: c for n, c in removed.items() if not code_has(n)}

    for doc in docs:
        try:
            lines = open(doc, errors="replace").read().splitlines()
        except OSError:
            continue
        in_fence = False
        for i, text in enumerate(lines, 1):
            if text.strip().startswith("```"):
                in_fence = not in_fence
                continue
            for old, new in moved.items():
                if old in text or (os.path.basename(old) in text and len(os.path.basename(old)) > 8 and "/" in old and f"`{os.path.basename(old)}`" in text):
                    add("moved-path", doc, i, old, f"{old} was {'renamed to ' + new if new else 'deleted'} since {since[:10]}",
                        prio=1)
            for name, sha in removed.items():
                if re.search(rf"\b{re.escape(name)}\b", text):
                    add("removed-symbol", doc, i, name, f"{name} was deleted in {sha} and no code defines it now",
                        commit=sha, prio=1)
            if in_fence:
                continue
            toks = re.findall(r"`([^`\n]{2,120})`", text) + re.findall(r"\]\(([^)#\s]+)\)", text)
            for t in toks:
                t = t.strip()
                if t.startswith(("http", "mailto:", "~", "/", "$", "<", "#")) or any(ch in t for ch in "*<>{} "):
                    continue
                if "/" in t and re.fullmatch(r"[\w.\-/]+", t) and ("." in t.split("/")[-1] or t.endswith("/")):
                    p = os.path.normpath(os.path.join(os.path.dirname(doc), t)) if t.startswith(".") else t.rstrip("/")
                    alt = os.path.normpath(os.path.join(os.path.dirname(doc), t)).rstrip("/")
                    if not os.path.exists(p) and not os.path.exists(alt) and not path_known(t) \
                            and not re.search(r"\.(example|sample)$", p):
                        add("missing-path", doc, i, t, f"{t} does not exist at {head[:10]}", prio=2)
                    continue
                if re.fullmatch(r"--[a-z][a-z0-9\-]{2,}", t):
                    flag = t[2:]
                    if not code_has(t, fixed=True) and not code_has(f'"{flag}"', fixed=True):
                        add("missing-flag", doc, i, t, f"no code file mentions {t} or \"{flag}\"", prio=3)
                    continue
                m = re.fullmatch(r"([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)(\(\))?", t)
                if not m:
                    continue
                ident = m[1]
                last = ident.split(".")[-1]
                codeish = m[2] or "." in ident or "_" in last or re.search(r"[a-z][A-Z]", last)
                if not codeish or last in COMMON or len(last) < 4 or re.fullmatch(r"[A-Z0-9_]+", last):
                    continue
                if re.fullmatch(r"[\w\-]+\.(md|yaml|yml|json|go|py|ts|js|sh|txt|toml)", ident):
                    continue  # a bare filename, not a symbol
                if not code_has(last):
                    add("missing-symbol", doc, i, t, f"no code file at {head[:10]} contains the word {last}", prio=3)

    cands.sort(key=lambda c: (c["prio"], c["doc"], c["line"]))
    kept = cands[:MAX]
    with open(f"{D}/candidates.jsonl", "w") as f:
        for c in kept:
            f.write(json.dumps(c) + "\n")
    by = {}
    for c in cands:
        by[c["kind"]] = by.get(c["kind"], 0) + 1
    print(f"since {since[:10]} ({len(commits)} commits) · {len(docs)} docs in scope · {len(moved)} paths moved/deleted · "
          f"{len(removed)} definitions removed · {len(cands)} drift candidates {by}" +
          (f" · first {MAX} kept" if len(cands) > MAX else ""))
    for c in kept:
        print(f"  [{c['kind']}] {c['doc']}:{c['line']}  {c['token']}  — {c['detail']}"[:230])
    sys.exit(0 if kept else 78)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"collect.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
