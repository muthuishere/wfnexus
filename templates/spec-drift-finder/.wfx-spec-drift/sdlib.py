"""sdlib.py — shared by judge.py and verify.py: evidence is built the SAME way the
first time a requirement is judged and when it is re-judged after a change.

Evidence is facts a machine read, never an agent's paraphrase: the requirement
as it stands in the spec file, the cited code/test lines read from disk, git
history of both, and other spec statements naming the same tokens.
"""
import json
import os
import re
import subprocess

D = ".wfx-spec-drift"
REF_RE = re.compile(r"^\s*`?([^\s:`]+?):(\d+)(?:[-–](\d+))?`?")


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout


def load_jsonl(path):
    out = []
    if not os.path.exists(path):
        return out
    for l in open(path, encoding="utf-8", errors="replace"):
        l = l.strip()
        if l:
            try:
                out.append(json.loads(l))
            except ValueError:
                pass
    return out


def requirements():
    return {r["id"]: r for r in load_jsonl(f"{D}/requirements.jsonl")}


def parse_ref(ref):
    """'path:12' / 'path:12-20' -> (path, start, end) or None."""
    m = REF_RE.match(str(ref))
    if not m:
        return None
    start = int(m[2])
    end = int(m[3]) if m[3] else start
    return m[1].lstrip("./"), start, max(start, end)


def read_ref(ref, pad=3, cap=24):
    """The cited lines, read from the file. Returns (label, text) or (label, None) when the ref is bad."""
    p = parse_ref(ref)
    if not p:
        return str(ref), None
    path, s, e = p
    if not os.path.isfile(path):
        return f"{path}:{s}", None
    lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
    if s < 1 or s > len(lines):
        return f"{path}:{s}", None
    a, b = max(1, s - pad), min(len(lines), max(e, s) + pad, s + cap)
    body = "\n".join(f"    {i:5d}  {lines[i - 1][:220]}" for i in range(a, b + 1))
    return f"{path}:{s}" + (f"-{e}" if e != s else ""), body


def last_commit(path):
    return git("log", "-1", "--format=%h %cs %s", "--", path).strip()


def commits_after(path, since_date):
    if not since_date:
        return []
    return git("log", f"--since={since_date}", "--format=%h %cs %s", "-n", "3", "--", path).strip().splitlines()


def related(req, all_reqs, limit=3):
    """Other spec statements sharing a specific token — how a conflict becomes visible."""
    mine = {h["token"] for h in req.get("hints", []) if 0 < h.get("total", 0) < 40 or h.get("total", 0) == 0}
    mine = {t for t in mine if len(t) >= 4}
    if not mine:
        return []
    scored = []
    for o in all_reqs.values():
        if o["id"] == req["id"]:
            continue
        theirs = {h["token"] for h in o.get("hints", [])}
        n = len(mine & theirs)
        if n:
            scored.append((n, o["spec"] != req["spec"], o))
    scored.sort(key=lambda x: (-x[0], not x[1]))
    return [o for _, _, o in scored[:limit]]


def evidence(req, mapping, all_reqs, text_override=None, where_override=None, extra=None):
    """The state the classifier sees, in plain sentences."""
    spec_last = last_commit(req["spec"])
    spec_date = spec_last.split(" ")[1] if spec_last.count(" ") >= 1 else ""
    text = text_override or req["text"]
    where = where_override or req["where"]
    L = [f"Requirement {req['id']} ({req['kind']}, from a {req['spec_kind']}) at {where}"
         + (f", section \"{req.get('section')}\"" if req.get("section") else "") + ":",
         f"  \"{text}\"",
         f"Spec file last changed: {spec_last or 'unknown'}."]
    if mapping is None:
        L.append("No mapper reading was recorded for this requirement. Machine search hits for the tokens it names:")
        for h in req.get("hints", [])[:5]:
            L.append(f"  `{h['token']}`: {h['total']} hits" + (": " + "; ".join(h["hits"][:3]) if h["hits"] else ""))
    else:
        L.append(f"Mapper's reading: status={mapping.get('status')}. {mapping.get('notes', '')}".strip())
        if mapping.get("status") == "not_code":
            L.append("The mapper found that this is NOT behaviour this repository's code implements: it is guidance, "
                     "or it describes a third-party format or library this repository only uses.")
        bad = []
        for kind in ("code_refs", "test_refs"):
            refs = mapping.get(kind) or []
            if not refs:
                continue
            L.append("Code cited (read from the files):" if kind == "code_refs" else "Tests cited (read from the files):")
            for ref in refs[:5]:
                label, body = read_ref(ref)
                if body is None:
                    bad.append(label)
                    continue
                p = parse_ref(ref)
                lc = last_commit(p[0]) if p else ""
                L.append(f"  {label} (file last changed {lc}):\n{body}")
                later = commits_after(p[0], spec_date) if p else []
                if later:
                    L.append(f"  Commits to {p[0]} after the spec's last change: " + "; ".join(later))
        if bad:
            L.append("Cited locations that do NOT exist in the repository (ignore them): " + ", ".join(bad))
        if mapping.get("moved_on"):
            L.append(f"Evidence the code moved on deliberately, per the mapper: {mapping['moved_on']}")
    for o in related(req, all_reqs):
        L.append(f"Another spec statement naming the same tokens — {o['id']} at {o['where']}: \"{o['text'][:300]}\"")
    if extra:
        L.extend(extra)
    return "\n".join(L)


def judge(items, questions=f"{D}/verdict-questions.yaml"):
    """items: [{'id', 'state'}] -> {id: result}. Raises on a classifier failure."""
    path = f"{D}/.judge-items.jsonl"
    with open(path, "w") as f:
        for it in items:
            f.write(json.dumps(it) + "\n")
    j = subprocess.run(["wfx", "judge", "-q", questions, "--items", path, "--parallel", "8"],
                       capture_output=True, text=True, timeout=1800)
    if j.returncode and not j.stdout.strip():
        raise RuntimeError("the classifier could not judge: " + j.stderr.strip()[:600] +
                           " (it needs TYPESAFE_API_KEY or OPENROUTER_API_KEY)")
    return {r["id"]: r for r in (json.loads(l) for l in j.stdout.splitlines() if l.strip())}


def answer(res, q):
    a = (res or {}).get("answers", {}).get(q) or {}
    return a.get("choice"), a.get("band"), a.get("confidence")
