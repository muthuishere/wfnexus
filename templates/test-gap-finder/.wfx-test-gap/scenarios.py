#!/usr/bin/env python3
"""scenarios.py — hold the analysts to the surface, by code.

    python3 scenarios.py check F3      # an analyst runs this on its own file until it exits 0
    python3 scenarios.py todo 2        # what shard 2 must cover: files, items, test files
    python3 scenarios.py merge         # after the shards: every item accounted, or a human sees it

An analyst writes .wfx-test-gap/scenarios/F<n>.json for each source file:

  {"file": "src/x.py",
   "scenarios": [{"id": "F3-S01", "items": ["F3-004", "F3-005"],
                  "kind": "error|boundary|edge|null|untested_public|doc_promise|spec_promise|combination|concurrency|async",
                  "title": "...", "given_when_then": "...", "expected": "what the code or its docs promise",
                  "evidence": "src/x.py:42", "status": "gap" | "covered",
                  "covered_by": "<test id or test function name, when covered>"}],
   "not_applicable": [{"item": "F3-010", "why": "..."}]}

`check` refuses: an item of the file that no scenario and no not_applicable
entry names; an item id that does not exist; evidence that is not a real
file:line; a `covered` claim naming a test that does not exist — or one that is
RED at baseline (a failing test pins nothing); ids that are not unique.

`merge` then turns every item still unaccounted for (an analyst that ran out of
turns, a file nobody wrote) into an `unaddressed` scenario, which triage sends
to a human. Nothing is dropped silently.
"""
import glob
import json
import os
import re
import sys

D = ".wfx-test-gap"
KINDS = {"error", "boundary", "edge", "null", "untested_public", "doc_promise", "spec_promise", "combination",
         "concurrency", "async"}


def surface():
    return [json.loads(l) for l in open(f"{D}/surface.jsonl") if l.strip()]


def baseline_tests():
    try:
        return json.load(open(f"{D}/before.json")).get("tests", {})
    except (OSError, ValueError):
        return {}


def test_files():
    style = json.load(open(f"{D}/style.json"))
    out = []
    for t in style.get("test_dirs") or []:
        for dp, dn, fn in os.walk(t):
            dn[:] = [d for d in dn if d not in ("node_modules", "__pycache__") and not d.startswith(".")]
            out += [os.path.join(dp, f) for f in fn if re.search(r"\.(py|ts|tsx|js|mjs|go|sh|bats)$", f)]
    return out


def find_test(name, tests, files):
    """(status, where) for a claimed covering test; status None when it does not exist."""
    name = (name or "").strip().strip("`")
    if not name:
        return None, ""
    if name in tests:
        return tests[name], name
    leaf = re.split(r"::|/|\.| > ", name)[-1].strip() or name
    for tid, st in tests.items():
        if tid.endswith(name) or tid.endswith("::" + leaf) or tid.endswith("/" + leaf) or tid.endswith(" " + name) \
                or name in tid:
            return st, tid
    for f in files:  # a test the runner did not report (e.g. collection error) still exists in source
        try:
            if re.search(r"\b" + re.escape(leaf) + r"\b", open(f, errors="replace").read()):
                return "unknown", f
        except OSError:
            pass
    return None, ""


def valid_evidence(ev):
    m = re.match(r"^`?([^:`]+):(\d+)", (ev or "").strip())
    if not m or not os.path.isfile(m[1]):
        return False
    return 1 <= int(m[2]) <= sum(1 for _ in open(m[1], errors="replace"))


def fidx_for(fid):
    shards = json.load(open(f"{D}/shards.json"))
    for f, meta in shards["files"].items():
        if f"F{meta['n']}" == fid:
            return f
    return None


def check(fid, quiet=False):
    path = fidx_for(fid)
    if path is None:
        print(f"{fid} is not a file id in shards.json")
        return 2
    items = {i["id"]: i for i in surface() if i["file"] == path}
    out = f"{D}/scenarios/{fid}.json"
    problems = []
    if not os.path.exists(out):
        print(f"{out} does not exist yet — write it (see `python3 {D}/scenarios.py todo`)")
        return 1
    try:
        doc = json.load(open(out))
    except ValueError as e:
        print(f"{out} is not valid JSON: {e}")
        return 1
    tests, files = baseline_tests(), test_files()
    seen_ids, accounted = set(), set()
    for s in doc.get("scenarios", []):
        sid = s.get("id", "")
        if not re.match(rf"^{fid}-S\d+$", sid):
            problems.append(f"scenario id {sid!r} must look like {fid}-S01")
        if sid in seen_ids:
            problems.append(f"duplicate scenario id {sid}")
        seen_ids.add(sid)
        for it in s.get("items", []):
            if it not in items:
                problems.append(f"{sid}: item {it} is not an item of {path}")
            accounted.add(it)
        if s.get("kind") not in KINDS:
            problems.append(f"{sid}: kind {s.get('kind')!r} is not one of {sorted(KINDS)}")
        if not valid_evidence(s.get("evidence")):
            problems.append(f"{sid}: evidence {s.get('evidence')!r} is not a real path:line")
        if s.get("status") not in ("gap", "covered"):
            problems.append(f"{sid}: status must be gap or covered")
        if s.get("status") == "covered":
            st, where = find_test(s.get("covered_by"), tests, files)
            if st is None:
                problems.append(f"{sid}: covered_by {s.get('covered_by')!r} names no test that exists — "
                                f"give the real test id, or mark it a gap")
            elif st in ("failed", "error"):
                problems.append(f"{sid}: covered_by {where} is RED at baseline, so it pins nothing — mark it a gap")
        if s.get("status") == "gap" and not (s.get("given_when_then") and s.get("expected")):
            problems.append(f"{sid}: a gap needs given_when_then and expected")
    for na in doc.get("not_applicable", []):
        if na.get("item") not in items:
            problems.append(f"not_applicable item {na.get('item')} is not an item of {path}")
        elif not (na.get("why") or "").strip():
            problems.append(f"not_applicable {na.get('item')} needs a why")
        accounted.add(na.get("item"))
    missing = [i for i in items if i not in accounted]
    for i in missing:
        it = items[i]
        problems.append(f"UNACCOUNTED {i} ({it['kind']} {it['symbol']} line {it['line']}): {it['detail'][:90]}")
    if problems:
        print(f"{fid} {path}: {len(problems)} problem(s) — fix them and run check again:")
        for p in problems:
            print("  -", p)
        return 1
    if not quiet:
        g = sum(1 for s in doc["scenarios"] if s["status"] == "gap")
        print(f"{fid} {path}: OK — {len(items)} items accounted; {g} gap(s), "
              f"{len(doc['scenarios']) - g} covered, {len(doc.get('not_applicable', []))} not applicable")
    return 0


def todo(shard):
    sh = json.load(open(f"{D}/shards.json"))
    files = sh["shards"].get(str(shard), [])
    by = {}
    for i in surface():
        by.setdefault(i["file"], []).append(i)
    print(f"shard {shard}: {len(files)} file(s). Existing tests live in: {', '.join(sorted(set(os.path.dirname(f) for f in test_files()))) or '(none found)'}")
    for f in files:
        m = sh["files"][f]
        print(f"\n== F{m['n']} {f} — {m['items']} items, line coverage {m['coverage']}% → write {D}/scenarios/F{m['n']}.json")
        for i in by.get(f, []):
            ex = "" if "line_executed_by_suite" not in i else (" [line run by suite]" if i["line_executed_by_suite"] else " [LINE NEVER RUN BY ANY TEST]")
            print(f"  {i['id']} {i['kind']:<18} {i['symbol']} :{i['line']}{ex} — {i['detail'][:140]}".replace("\n", " "))
    return 0


def merge():
    items = surface()
    sh = json.load(open(f"{D}/shards.json"))
    by_file = {}
    for i in items:
        by_file.setdefault(i["file"], []).append(i)
    tests, files = baseline_tests(), test_files()
    allsc, report = [], []
    for f, its in sorted(by_file.items()):
        fid = f"F{sh['files'][f]['n']}"
        p = f"{D}/scenarios/{fid}.json"
        doc = {}
        if os.path.exists(p):
            try:
                doc = json.load(open(p))
            except ValueError:
                doc = {}
        ok = check(fid, quiet=True) == 0 if doc else False
        accounted, n = set(), 0
        for s in doc.get("scenarios", []):
            if not isinstance(s, dict):
                continue
            s = {**s, "file": f}
            # an analyst's malformed row still counts if it says enough to triage;
            # otherwise its items stay unaccounted and a person sees them
            if s.get("status") not in ("gap", "covered"):
                if s.get("given_when_then") and s.get("expected"):
                    s["status"] = "gap"
                else:
                    continue
            if s.get("kind") not in KINDS:
                s["kind"] = "edge"
            if not valid_evidence(s.get("evidence")):
                s["evidence"] = f"{f}:1"
            s.setdefault("title", s.get("given_when_then", "")[:100])
            s["items"] = [i for i in s.get("items", []) if isinstance(i, str)]
            accounted.update(s["items"])
            if s.get("status") == "covered":
                st, where = find_test(s.get("covered_by"), tests, files)
                if st is None or st in ("failed", "error"):
                    s.update(status="gap", demoted=f"claimed covered by {s.get('covered_by')!r}, but that test "
                                                  + ("does not exist" if st is None else f"is {st} at baseline"))
                else:
                    s["covered_by"] = where
            allsc.append(s)
            n += 1
        accounted.update(na.get("item") for na in doc.get("not_applicable", []) if isinstance(na, dict) and (na.get("why") or "").strip())
        lost = [i for i in its if i["id"] not in accounted]
        for k, i in enumerate(lost, 1):
            allsc.append({"id": f"{fid}-U{k:02d}", "file": f, "items": [i["id"]], "kind": "edge", "status": "unaddressed",
                          "title": f"{i['kind']} in {i['symbol']} was not analysed",
                          "given_when_then": i["detail"], "expected": "unknown — no analyst accounted for it",
                          "evidence": f"{f}:{i['line']}"})
        report.append((fid, f, len(its), n, len(doc.get("not_applicable", [])), len(lost), ok))
    with open(f"{D}/scenarios.jsonl", "w") as fh:
        for s in allsc:
            fh.write(json.dumps(s) + "\n")
    tot = {k: sum(1 for s in allsc if s["status"] == k) for k in ("gap", "covered", "unaddressed")}
    print(f"{len(items)} surface items → {len(allsc)} scenarios: {tot['gap']} gap, {tot['covered']} already covered "
          f"(by a green test), {tot['unaddressed']} unaddressed (go to a human)")
    for fid, f, n_it, n_sc, n_na, n_lost, ok in report:
        print(f"  {fid:<4} {f}: {n_it} items, {n_sc} scenarios, {n_na} n/a, {n_lost} unaccounted"
              + ("" if ok else "  ← analyst's file failed the check"))
    demoted = [s for s in allsc if s.get("demoted")]
    for s in demoted:
        print(f"  demoted {s['id']}: {s['demoted']}")
    return 0 if (tot["gap"] + tot["unaddressed"]) else 78


if __name__ == "__main__":
    try:
        cmd = sys.argv[1] if len(sys.argv) > 1 else ""
        if cmd == "check" and len(sys.argv) > 2:
            sys.exit(max(check(a) for a in sys.argv[2:]))
        if cmd == "todo" and len(sys.argv) > 2:
            sys.exit(todo(sys.argv[2]))
        if cmd == "merge":
            sys.exit(merge())
        print(__doc__)
        sys.exit(2)
    except SystemExit:
        raise
    except Exception as e:
        import traceback
        traceback.print_exc()
        print(f"scenarios.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
