#!/usr/bin/env python3
"""collect.py — every spec in the repo, split into atomic, checkable requirements. No model.

Finds (tracked files only):
  openspec/specs/**/spec.md               the living specs
  openspec/changes/<open>/**              proposal, design, tasks and spec deltas of OPEN
                                          changes (openspec/changes/archive/** is history)
  specs/**, **/*.spec.md, **/SPEC*.md,    plain spec documents
  **/*-spec.md, **/contracts/**/*.md
  docs/adr/**, **/adr/**                  ADR decisions (superseded/rejected ADRs are skipped)
  openapi*/swagger* (yaml|json),          API contracts
  **/*.schema.json
  + EXTRA_SPECS                           anything else the person names

and splits each into requirements, each with file:line:
  shall      a SHALL / MUST / MUST NOT / SHALL NOT sentence (one sentence = one requirement)
  scenario   an OpenSpec `#### Scenario:` with its WHEN/THEN lines
  task       a checked `- [x]` task of an open change (it claims the work is done)
  decision   a statement under an ADR's `## Decision` section
  rule       a bullet / numbered rule / table row of a plain spec document
  api        an OpenAPI path + method; a JSON-schema required property

For every requirement it also records machine HINTS: each concrete token the text
names (`code`, --flags, /routes, paths, numbers with units) and where that token
appears in non-spec tracked files (git grep), so the mapper starts from facts.

Env: BRANCH, EXTRA_SPECS, EXCLUDE (comma globs), MAX_REQUIREMENTS (0 = all), TEST_CMD.
Writes .wfx-spec-drift/{requirements.jsonl,specs.json,baseline.json}.
Exit 0 with requirements, 78 when the repo has no spec at all, 70 on error.
"""
import fnmatch
import json
import os
import re
import subprocess
import sys

D = ".wfx-spec-drift"
SPEC_GLOBS = ["openspec/specs/**/spec.md", "openspec/changes/*/proposal.md", "openspec/changes/*/design.md",
              "openspec/changes/*/tasks.md", "openspec/changes/*/specs/**/spec.md",
              "specs/**/*.md", "**/*.spec.md", "**/SPEC*.md", "**/*-spec.md", "**/contracts/**/*.md",
              "docs/adr/*.md", "**/adr/*.md", "**/decisions/*.md",
              "**/openapi*.yaml", "**/openapi*.yml", "**/openapi*.json", "**/swagger*.yaml",
              "**/swagger*.json", "**/*.schema.json"]
EXCLUDE = [g.strip() for g in (os.environ.get("EXCLUDE") or
           "openspec/changes/archive/**,**/node_modules/**,**/vendor/**,.wfx-*/**,**/README.md,**/INDEX.md,**/template*.md")
           .split(",") if g.strip()]
EXTRA = [g.strip() for g in (os.environ.get("EXTRA_SPECS") or "").split(",") if g.strip()]
MAX = int(os.environ.get("MAX_REQUIREMENTS") or 0)
NORMATIVE = re.compile(r"\b(SHALL|MUST|REQUIRED|SHALL NOT|MUST NOT)\b")
SOFT_NORMATIVE = re.compile(r"\b(must|shall|always|never|required|should|only|default|at most|at least|exactly)\b", re.I)


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout


def match(path, globs):
    for g in globs:
        if fnmatch.fnmatch(path, g):
            return True
        if g.startswith("**/") and fnmatch.fnmatch(path, g[3:]):
            return True
        # fnmatch's * crosses "/", so "docs/adr/*.md" would also match deeper files; fine for specs
    return False


def kind_of(path):
    if path.startswith("openspec/specs/"):
        return "openspec-spec"
    if path.startswith("openspec/changes/"):
        return "openspec-delta" if "/specs/" in path else "openspec-change"
    if re.search(r"(^|/)(adr|decisions)/", path):
        return "adr"
    if path.endswith((".yaml", ".yml", ".json")):
        return "contract"
    return "spec-doc"


def clean(s):
    s = re.sub(r"\*\*(.+?)\*\*", r"\1", s)
    return re.sub(r"\s+", " ", s).strip()


def sentences(text):
    parts = re.split(r"(?<=[.;!?])\s+(?=[A-Z`(])", text)
    return [p.strip() for p in parts if p.strip()]


def md_blocks(lines):
    """Yield (line_no, heading_path, kind, text) for paragraphs, list items and table rows,
    skipping fenced code. heading_path is the list of enclosing headings."""
    heads, fence, para, para_start = [], False, [], 0
    out = []

    def flush():
        nonlocal para
        if para:
            out.append((para_start, list(heads), "para", clean(" ".join(para))))
        para = []

    for i, raw in enumerate(lines, 1):
        s = raw.rstrip("\n")
        if s.strip().startswith("```") or s.strip().startswith("~~~"):
            flush()
            fence = not fence
            continue
        if fence:
            continue
        m = re.match(r"^(#{1,6})\s+(.*)", s)
        if m:
            flush()
            lvl = len(m[1])
            heads = [h for h in heads if h[0] < lvl] + [(lvl, clean(m[2]))]
            out.append((i, list(heads), "heading", clean(m[2])))
            continue
        if not s.strip():
            flush()
            continue
        li = re.match(r"^(\s*)(?:[-*+]|\d+[.)])\s+(.*)", s)
        if li:
            flush()
            out.append((i, list(heads), "item", clean(li[2])))
            continue
        if s.strip().startswith("|"):
            flush()
            cells = [c.strip() for c in s.strip().strip("|").split("|")]
            if all(re.fullmatch(r":?-{2,}:?", c) for c in cells if c):
                continue
            out.append((i, list(heads), "row", " | ".join(cells)))
            continue
        # continuation of a list item (indented) joins the item
        if out and out[-1][2] == "item" and raw.startswith((" ", "\t")) and not para:
            ln, hp, k, t = out[-1]
            out[-1] = (ln, hp, k, t + " " + clean(s))
            continue
        if not para:
            para_start = i
        para.append(s)
    flush()
    return merge_lead_ins(out)


def merge_lead_ins(blocks):
    """"X covers:" followed by bullets is ONE requirement ("X covers: a; b; c"),
    not a dangling lead-in plus fragments nobody can check on their own."""
    out = []
    for b in blocks:
        if (b[2] == "item" and out and out[-1][2] in ("para", "item", "lead")
                and (out[-1][2] == "lead" or out[-1][3].endswith(":"))):
            ln, hp, k, t = out[-1]
            sep = " " if k != "lead" else "; "
            out[-1] = (ln, hp, "lead", t + sep + b[3])
            continue
        out.append(b)
    return [(ln, hp, "item" if k == "lead" else k, t) for ln, hp, k, t in out]


def hpath(heads):
    return " > ".join(h[1] for h in heads)


def parse_openspec(path, lines, reqs):
    """Requirement blocks: SHALL/MUST sentences + one requirement per scenario."""
    cur_req, delta_op, scen = None, "", None

    def close_scen():
        nonlocal scen
        if scen:
            reqs.append({"kind": "scenario", "line": scen["line"], "section": cur_req or "",
                         "delta": delta_op, "text": f"Scenario: {scen['name']} — " + " ".join(scen["body"])})
        scen = None

    for ln, heads, k, text in md_blocks(lines):
        if k == "heading":
            lvl = heads[-1][0]
            if lvl == 2:
                close_scen()
                m = re.match(r"(ADDED|MODIFIED|REMOVED|RENAMED) Requirements", text, re.I)
                delta_op = m[1].upper() if m else ""
                cur_req = None
            elif lvl == 3:
                close_scen()
                cur_req = re.sub(r"^Requirement:\s*", "", text)
                if delta_op == "REMOVED":
                    reqs.append({"kind": "shall", "line": ln, "section": cur_req, "delta": delta_op,
                                 "text": f"REMOVED requirement: {cur_req} — the behaviour SHALL no longer exist"})
            elif lvl == 4 and text.lower().startswith("scenario"):
                close_scen()
                scen = {"line": ln, "name": re.sub(r"^Scenario:\s*", "", text, flags=re.I), "body": []}
            continue
        if scen is not None:
            scen["body"].append(text)
            continue
        if cur_req is None or delta_op == "REMOVED":
            continue
        for s in sentences(text):
            if NORMATIVE.search(s):
                reqs.append({"kind": "shall", "line": ln, "section": cur_req, "delta": delta_op, "text": s})
    close_scen()


def parse_change(path, lines, reqs):
    """proposal/design/tasks of an open change: checked tasks, What Changes bullets, decisions."""
    base = os.path.basename(path)
    for ln, heads, k, text in md_blocks(lines):
        if k == "heading":
            continue
        hp = hpath(heads)
        if base == "tasks.md":
            m = re.match(r"^\[([ xX])\]\s*(.*)", text)
            if m and m[1] in "xX":
                reqs.append({"kind": "task", "line": ln, "section": hp, "text": "Done (checked task): " + m[2]})
            continue
        if NORMATIVE.search(text):
            for s in sentences(text):
                if NORMATIVE.search(s):
                    reqs.append({"kind": "shall", "line": ln, "section": hp, "text": s})
            continue
        if k == "item" and re.search(r"what changes|decision|goals", hp, re.I) and not re.search(r"non-goal|alternative|risk", hp, re.I):
            reqs.append({"kind": "decision", "line": ln, "section": hp, "text": text})


def adr_status(lines):
    for l in lines[:40]:
        m = re.search(r"status\W*\s*(accepted|proposed|superseded|deprecated|rejected|draft)", l, re.I)
        if m:
            return m[1].lower()
    return "unknown"


def parse_adr(path, lines, reqs):
    st = adr_status(lines)
    if st in ("superseded", "deprecated", "rejected"):
        return st
    for ln, heads, k, text in md_blocks(lines):
        if k == "heading" or not heads:
            continue
        if not any(re.match(r"decision", h[1], re.I) for h in heads if h[0] >= 2):
            continue
        if k == "row" and not re.search(r"[a-z]", text):
            continue
        if k == "para":
            for s in sentences(text):
                if len(s) >= 25:
                    reqs.append({"kind": "decision", "line": ln, "section": hpath(heads), "text": s})
        elif len(text) >= 15:
            reqs.append({"kind": "decision", "line": ln, "section": hpath(heads), "text": text})
    return st


def parse_doc(path, lines, reqs):
    """A plain spec document: rules (bullets, numbered items, table rows) and normative sentences."""
    header_rows = set()
    blocks = md_blocks(lines)
    # the first row of each table is its header
    prev = None
    for b in blocks:
        if b[2] == "row" and (prev is None or prev[2] != "row" or prev[0] < b[0] - 2):
            header_rows.add(b[0])
        prev = b
    for ln, heads, k, text in blocks:
        if k == "heading" or len(text) < 12:
            continue
        hp = hpath(heads)
        if re.search(r"\b(example|examples|see also|references|changelog|history|background|motivation)\b", hp, re.I):
            continue
        if k == "row":
            if ln in header_rows:
                continue
            reqs.append({"kind": "rule", "line": ln, "section": hp, "text": text})
        elif k == "item":
            if re.match(r"^\[[ xX]\]", text) or text.endswith(":"):
                continue
            reqs.append({"kind": "rule", "line": ln, "section": hp, "text": text})
        else:
            for s in sentences(text):
                if NORMATIVE.search(s) or (SOFT_NORMATIVE.search(s) and re.search(r"`|\d", s)):
                    reqs.append({"kind": "rule", "line": ln, "section": hp, "text": s})


def parse_contract(path, lines, reqs):
    text = "".join(lines)
    try:
        if path.endswith(".json"):
            doc = json.loads(text)
        else:
            import yaml  # optional
            doc = yaml.safe_load(text)
    except Exception:
        doc = None

    def line_of(needle):
        for i, l in enumerate(lines, 1):
            if needle in l:
                return i
        return 1

    if isinstance(doc, dict) and isinstance(doc.get("paths"), dict):
        for p, ops in doc["paths"].items():
            for m, op in (ops or {}).items():
                if m.lower() in ("get", "post", "put", "patch", "delete", "head", "options"):
                    summ = (op or {}).get("summary") or (op or {}).get("operationId") or ""
                    reqs.append({"kind": "api", "line": line_of(p), "section": p,
                                 "text": f"The API SHALL expose {m.upper()} {p}" + (f" — {summ}" if summ else "")})
        return
    if isinstance(doc, dict) and ("$schema" in doc or "properties" in doc):
        def walk(node, where):
            if not isinstance(node, dict):
                return
            for r in node.get("required", []) or []:
                reqs.append({"kind": "api", "line": line_of(f'"{r}"'), "section": where or "(root)",
                             "text": f"Schema {where or '(root)'} SHALL require property `{r}`"})
            for k, v in (node.get("properties") or {}).items():
                walk(v, f"{where}.{k}" if where else k)
        walk(doc, "")
        return
    # unparseable (no PyYAML): fall back to path lines of an OpenAPI file
    for i, l in enumerate(lines, 1):
        m = re.match(r"^  (/[^\s:]*):\s*$", l)
        if m:
            reqs.append({"kind": "api", "line": i, "section": m[1], "text": f"The API SHALL expose {m[1]}"})


TOKEN_RE = [
    re.compile(r"`([^`\n]{2,80})`"),
    re.compile(r"(?<![\w-])(--[a-z][a-z0-9-]{1,40})"),
    re.compile(r"(?<![\w/])(/(?:v\d+|api)[/\w{}.-]*)"),
    re.compile(r"\b(\d+(?:\.\d+)?\s?(?:ms|s|sec|seconds|minutes|MB|KB|px|%))\b"),
]


def tokens(text):
    out = []
    for rx in TOKEN_RE:
        for m in rx.finditer(text):
            t = m[1].strip()
            if 2 <= len(t) <= 80 and t not in out:
                out.append(t)
    return out[:8]


def main():
    branch = os.environ.get("BRANCH")
    head = git("rev-parse", "HEAD").strip()
    if not head:
        print("not a git checkout — spec-drift-finder needs repo_path or repo_url", file=sys.stderr)
        sys.exit(70)
    if branch and subprocess.run(["git", "checkout", "-b", branch], capture_output=True).returncode:
        subprocess.run(["git", "checkout", branch], capture_output=True)

    tracked = git("ls-files").splitlines()
    specs = [p for p in tracked if (match(p, SPEC_GLOBS) or match(p, EXTRA) or p in EXTRA) and not match(p, EXCLUDE)]
    spec_set = set(specs)
    code_files = [p for p in tracked if p not in spec_set and not p.endswith((".md", ".lock", ".sum"))
                  and not p.startswith((".wfx-", "openspec/"))]

    reqs_all, spec_meta, skipped = [], [], []
    for path in specs:
        try:
            lines = open(path, encoding="utf-8", errors="replace").readlines()
        except OSError:
            continue
        kind = kind_of(path)
        reqs = []
        status = ""
        if kind in ("openspec-spec", "openspec-delta"):
            parse_openspec(path, lines, reqs)
        elif kind == "openspec-change":
            parse_change(path, lines, reqs)
        elif kind == "adr":
            status = parse_adr(path, lines, reqs)
            if status in ("superseded", "deprecated", "rejected"):
                skipped.append({"spec": path, "why": f"ADR status {status} — history, not a live decision"})
                continue
        elif kind == "contract":
            parse_contract(path, lines, reqs)
        else:
            parse_doc(path, lines, reqs)
        last = git("log", "-1", "--format=%h %cs %s", "--", path).strip()
        spec_meta.append({"spec": path, "kind": kind, "status": status, "requirements": len(reqs), "last_commit": last})
        for r in reqs:
            r.update(spec=path, spec_kind=kind)
            reqs_all.append(r)

    if not specs:
        print("No spec files found (openspec/specs, open openspec/changes, specs/, *.spec.md, SPEC*.md, "
              "ADRs, OpenAPI/JSON-schema contracts). Point `extra_specs` at the documents this repo treats as specs.")
        json.dump({"specs": [], "skipped": skipped}, open(f"{D}/specs.json", "w"), indent=1)
        sys.exit(78)

    truncated = 0
    if MAX and len(reqs_all) > MAX:
        truncated = len(reqs_all) - MAX
        reqs_all = reqs_all[:MAX]

    code_grep = [p for p in code_files][:20000]
    with open(f"{D}/requirements.jsonl", "w") as f:
        for n, r in enumerate(reqs_all, 1):
            r["id"] = f"R{n:03d}"
            hints = []
            for t in tokens(r["text"]):
                g = subprocess.run(["git", "grep", "-n", "-I", "-F", "-e", t, "--", "."] +
                                   [f":!{s}" for s in specs[:200]] + [":!*.md", ":!openspec/**"],
                                   capture_output=True, text=True).stdout.splitlines()
                hints.append({"token": t, "hits": [h[:200] for h in g[:6]], "total": len(g)})
            r["hints"] = hints
            r["where"] = f"{r['spec']}:{r['line']}"
            f.write(json.dumps(r) + "\n")

    by_spec = {}
    for r in reqs_all:
        by_spec.setdefault(r["spec"], []).append(r["id"])
    for m in spec_meta:
        m["ids"] = by_spec.get(m["spec"], [])
    json.dump({"head": head, "branch": branch, "specs": spec_meta, "skipped": skipped,
               "truncated": truncated, "total": len(reqs_all)}, open(f"{D}/specs.json", "w"), indent=1)

    # baseline suite, measured before anything changes
    test_cmd = (os.environ.get("TEST_CMD") or "").strip() or detect_test_cmd()
    base = {"base": head, "branch": branch, "test_cmd": test_cmd}
    if test_cmd:
        r = subprocess.run(test_cmd, shell=True, capture_output=True, text=True, timeout=3000)
        base.update(suite_exit=r.returncode, suite_tail=(r.stdout + r.stderr)[-1500:])
    json.dump(base, open(f"{D}/baseline.json", "w"), indent=1)

    print(f"base {head[:10]} · branch {branch} · {len(specs)} spec files · {len(reqs_all)} requirements"
          + (f" ({truncated} over max_requirements — NOT checked this run)" if truncated else ""))
    for m in spec_meta:
        print(f"  {m['requirements']:4d}  {m['spec']}  [{m['kind']}{' ' + m['status'] if m['status'] else ''}]")
    for s in skipped:
        print(f"  skip  {s['spec']} — {s['why']}")
    print(f"suite: `{test_cmd or 'none detected'}` → exit {base.get('suite_exit', 'n/a')}")
    if not reqs_all:
        print("Spec files exist but no checkable requirement was extracted.")
        sys.exit(78)


def detect_test_cmd():
    if os.path.exists("Taskfile.yml") or os.path.exists("Taskfile.yaml"):
        t = open("Taskfile.yml" if os.path.exists("Taskfile.yml") else "Taskfile.yaml").read()
        if re.search(r"^\s{2}test:\s*$", t, re.M):
            return "task test"
    if os.path.exists("go.mod"):
        return "go test ./..."
    if os.path.exists("Cargo.toml"):
        return "cargo test --quiet"
    if os.path.exists("package.json"):
        try:
            if "test" in json.load(open("package.json")).get("scripts", {}):
                return "npm test --silent"
        except ValueError:
            pass
    for d in (".", "scripts", "src"):
        if os.path.exists(os.path.join(d, "pyproject.toml")):
            return f"cd {d} && (uv run --with pytest python -m pytest -q 2>/dev/null || python3 -m pytest -q)"
    return ""


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"collect.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
