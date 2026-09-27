#!/usr/bin/env python3
"""collect.py — failed CI runs → root-cause groups, by rules, before any model.

For each failed run (one run from a repository_dispatch, or a sweep of
`gh run list --status failure`):

  * failed jobs and their logs (`gh run view --log-failed`), saved to logs/;
  * what failed, parsed per job: Go `--- FAIL:` tests and their packages,
    pytest `FAILED`, jest `●`, compiler errors (`file:line:col:`), `##[error]`
    lines, and known INFRA signatures (runner lost, disk full, network, rate
    limit, cancelled) — the first meaningful error, with the noise
    (deprecation warnings, "Process completed with exit code") skipped;
  * a FINGERPRINT per job: the sorted failing tests (or the normalised first
    error, or the infra signature) — the same failure on three OS jobs and in
    three consecutive runs is ONE root cause, so ONE issue;
  * history from the same workflow on the same branch: did the same commit
    pass on a rerun (flaky), which green run preceded the first red one, the
    commits in between and which of them touched the failing packages (the
    suspect), and whether the branch has recovered since — and with which
    commit;
  * open issues carrying the fingerprint marker, so a repeat is a comment,
    not a new issue.

Env: REPO (owner/name; default from gh), RUN_ID, BRANCH, LIMIT, LABEL,
LAST_RUN_ID (skip runs at or below it in a sweep), INCLUDE_RECOVERED.
Writes .wfx-ci-doctor/groups.json and logs/. Exit 78 when there is nothing to
look at, 70 on error.
"""
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys

D = ".wfx-ci-doctor"
LIMIT = int(os.environ.get("LIMIT") or 10)
LABEL = os.environ.get("LABEL") or "ci-failure"
MARK = "wfx-ci-fingerprint:"
INCLUDE_RECOVERED = (os.environ.get("INCLUDE_RECOVERED") or "false").lower() in ("1", "true", "yes")

INFRA = [
    ("runner-lost", r"runner has received a shutdown signal|lost communication with the server|The hosted runner encountered an error"),
    ("disk-full", r"No space left on device|ENOSPC"),
    ("network", r"Could not resolve host|ECONNRESET|ETIMEDOUT|connection reset by peer|TLS handshake timeout|i/o timeout.*(proxy|registry)|dial tcp .* (timeout|refused)"),
    ("rate-limit", r"rate limit|429 Too Many Requests|toomanyrequests|secondary rate limit"),
    ("registry-down", r"50[234] (Bad Gateway|Service Unavailable|Gateway Time-?out)|failed to download|error downloading|unexpected EOF.*(fetch|download)"),
    ("cancelled", r"The operation was canceled|exit code 143|received SIGTERM"),
    ("oom", r"exit code 137|Killed\s*$|out of memory|OOMKilled"),
]
NOISE = re.compile(r"DeprecationWarning|Process completed with exit code|^\s*$|##\[group\]|##\[endgroup\]|^warning:", re.I)
ERR = re.compile(r"(^|\s)(error|Error|ERROR|FAIL|panic:|fatal:|Traceback|Exception)\b|##\[error\]")


def gh(*a, ok=False):
    r = subprocess.run(["gh", *a], capture_output=True, text=True)
    if r.returncode and not ok:
        raise RuntimeError(f"gh {' '.join(a[:3])} failed: {r.stderr.strip()[:300]}")
    return r.stdout if r.returncode == 0 else ""


def git(*a):
    r = subprocess.run(["git", *a], capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else ""


def norm(s):
    s = re.sub(r"\d{4}-\d\d-\d\dT[\d:.]+Z", "", s)
    s = re.sub(r"(/home/runner/work|D:\\a|/Users/runner/work)[^\s:]*", "<path>", s)
    s = re.sub(r"0x[0-9a-f]+|[0-9a-f]{12,}", "<hex>", s)
    s = re.sub(r"\(\d+(\.\d+)?s\)|\d+(\.\d+)?s\b", "", s)
    s = re.sub(r":\d+(:\d+)?", ":N", s)
    return re.sub(r"\s+", " ", s).strip()


def parse_job(lines):
    """lines: the log text of one job (prefixes stripped)."""
    tests, pkgs, compile_errs, errors, infra = [], [], [], [], []
    detail = {}
    cur = None
    for i, l in enumerate(lines):
        m = re.match(r"^(\s*)--- FAIL: (\S+)", l)
        if m:
            if not m[1]:  # top-level test (subtests are indented)
                cur = m[2]
                tests.append(cur)
                detail[cur] = []
            continue
        m = re.match(r"^FAIL\s+(\S+)\s+[\d.]+s", l) or re.match(r"^FAIL\s+(\S+)\s+\[build failed\]", l)
        if m:
            pkgs.append(m[1])
            cur = None
            continue
        m = re.match(r"^FAILED (\S+?::\S+)", l)
        if m:
            tests.append(m[1])
        m = re.match(r"^\s*● (.+ › .+)$", l)
        if m:
            tests.append(m[1].strip())
        if cur is not None and len(detail[cur]) < 12 and l.strip():
            detail[cur].append(l.rstrip())
        if re.match(r"^\S+\.(go|ts|tsx|rs|java|kt|cs|py):\d+(:\d+)?: ", l) or re.search(r"error TS\d+:", l):
            compile_errs.append(l.strip())
        for name, pat in INFRA:
            if re.search(pat, l):
                infra.append(name)
        if ERR.search(l) and not NOISE.search(l):
            errors.append((i, l.strip()))
    first = errors[0][1] if errors else ""
    for t in tests[:1]:
        if detail.get(t):
            first = f"--- FAIL: {t}: " + " ".join(x.strip() for x in detail[t][:4])
    return {"tests": sorted(set(tests)), "packages": sorted(set(pkgs)), "compile_errors": compile_errs[:10],
            "infra": sorted(set(infra)), "first_error": first[:500], "test_output": {t: d for t, d in detail.items() if d},
            "error_lines": [e[1][:300] for e in errors[:15]]}


def fingerprint(workflow, p):
    if p["tests"]:
        key = "tests:" + "|".join(p["tests"])
    elif p["compile_errors"]:
        key = "compile:" + norm(p["compile_errors"][0])
    elif p["infra"]:
        key = "infra:" + ",".join(p["infra"])
    else:
        key = "error:" + norm(p["first_error"])
    return hashlib.sha1(f"{workflow}|{key}".encode()).hexdigest()[:12], key


def pkg_dir(pkg):
    parts = pkg.split("/")
    for i in range(len(parts)):
        cand = "/".join(parts[i:])
        if cand and os.path.isdir(cand):
            return cand
    return None


def commits_between(repo, a, b):
    """Commits in (a, b], with the files each touched. Local git first, else the API."""
    out = []
    if a and b and git("cat-file", "-e", f"{a}^{{commit}}") is not None and git("rev-parse", "-q", "--verify", f"{b}^{{commit}}"):
        for line in git("log", "--format=%H%x09%an%x09%s", f"{a}..{b}").splitlines():
            sha, author, subj = line.split("\t", 2)
            files = git("show", "--name-only", "--format=", sha).split()
            out.append({"sha": sha[:10], "author": author, "subject": subj, "files": files})
        if out or git("rev-parse", "-q", "--verify", f"{a}^{{commit}}"):
            return out
    if a and b:
        try:
            data = json.loads(gh("api", f"repos/{repo}/compare/{a}...{b}"))
            for c in data.get("commits", [])[-30:]:
                files = [f["filename"] for f in json.loads(gh("api", f"repos/{repo}/commits/{c['sha']}", ok=True) or "{}").get("files", [])]
                out.append({"sha": c["sha"][:10], "author": c["commit"]["author"]["name"],
                            "subject": c["commit"]["message"].splitlines()[0], "files": files})
        except Exception as e:  # history is evidence, not a requirement
            out.append({"sha": "?", "author": "", "subject": f"could not compare {a[:8]}..{b[:8]}: {e}", "files": []})
    return out


def main():
    os.makedirs(f"{D}/logs", exist_ok=True)
    repo = os.environ.get("REPO") or json.loads(gh("repo", "view", "--json", "nameWithOwner"))["nameWithOwner"]
    run_id = (os.environ.get("RUN_ID") or "").strip()
    branch = (os.environ.get("BRANCH") or "").strip()
    last_seen = int(os.environ.get("LAST_RUN_ID") or 0) if (os.environ.get("LAST_RUN_ID") or "").isdigit() else 0
    fields = "databaseId,headSha,headBranch,displayTitle,createdAt,event,workflowName,url,attempt,conclusion"
    if run_id:
        runs = [json.loads(gh("run", "view", run_id, "-R", repo, "--json", fields))]
    else:
        args = ["run", "list", "-R", repo, "--status", "failure", "--limit", str(LIMIT), "--json", fields]
        if branch:
            args += ["--branch", branch]
        runs = [r for r in json.loads(gh(*args)) if r["databaseId"] > last_seen]
    runs = [r for r in runs if r.get("conclusion") == "failure"]  # cancelled/skipped are not failures
    if not runs:
        print(f"no new failed runs in {repo}" + (f" on {branch}" if branch else "") + (f" after run {last_seen}" if last_seen else ""))
        json.dump({"repo": repo, "groups": [], "max_run_id": last_seen}, open(f"{D}/groups.json", "w"))
        sys.exit(78)

    history_cache = {}

    def history(workflow, br):
        k = (workflow, br)
        if k not in history_cache:
            history_cache[k] = json.loads(gh("run", "list", "-R", repo, "--workflow", workflow, "--branch", br,
                                             "--limit", "100", "--json", "databaseId,headSha,conclusion,createdAt,attempt,url",
                                             ok=True) or "[]")
        return history_cache[k]

    groups = {}
    for r in runs:
        jobs = json.loads(gh("run", "view", str(r["databaseId"]), "-R", repo, "--json", "jobs"))["jobs"]
        failed_jobs = [j for j in jobs if j.get("conclusion") == "failure"]
        log = gh("run", "view", str(r["databaseId"]), "-R", repo, "--log-failed", ok=True)
        open(f"{D}/logs/{r['databaseId']}.log", "w").write(log)
        per_job = {}
        for line in log.splitlines():
            parts = line.split("\t", 2)
            if len(parts) == 3:
                txt = re.sub(r"^\ufeff?\d{4}-\d\d-\d\dT[\d:.]+Z ?", "", parts[2])
                per_job.setdefault(parts[0], []).append(txt)
        if not per_job and failed_jobs:
            per_job = {j["name"]: [] for j in failed_jobs}
        for job, lines in per_job.items():
            p = parse_job(lines)
            fp, key = fingerprint(r["workflowName"], p)
            g = groups.setdefault(fp, {"fingerprint": fp, "key": key, "workflow": r["workflowName"], "runs": [],
                                       "jobs": set(), "tests": p["tests"], "packages": set(), "compile_errors": p["compile_errors"],
                                       "infra": p["infra"], "first_error": p["first_error"], "test_output": p["test_output"],
                                       "error_lines": p["error_lines"]})
            g["jobs"].add(job)
            g["packages"].update(p["packages"])
            if r["databaseId"] not in [x["id"] for x in g["runs"]]:
                g["runs"].append({"id": r["databaseId"], "sha": r["headSha"], "branch": r["headBranch"], "url": r["url"],
                                  "title": r["displayTitle"], "created": r["createdAt"], "attempt": r.get("attempt", 1),
                                  "log": f"{D}/logs/{r['databaseId']}.log"})

    out = []
    for g in groups.values():
        g["jobs"], g["packages"] = sorted(g["jobs"]), sorted(g["packages"])
        g["runs"].sort(key=lambda x: x["created"])
        first, last = g["runs"][0], g["runs"][-1]
        hist = sorted(history(g["workflow"], first["branch"]), key=lambda x: x["createdAt"])
        same_sha_green = [h for h in hist if h["headSha"] == first["sha"] and h["conclusion"] == "success"]
        before = [h for h in hist if h["createdAt"] < first["created"] and h["conclusion"] == "success"]
        after = [h for h in hist if h["createdAt"] > last["created"] and h["conclusion"] in ("success", "failure")]
        last_green = before[-1] if before else None
        recovered = next((h for h in after if h["conclusion"] == "success"), None)
        dirs = sorted({d for d in (pkg_dir(p) for p in g["packages"]) if d})
        suspects = commits_between(repo, last_green["headSha"] if last_green else None, first["sha"])
        for c in suspects:
            c["touches_failing_package"] = any(f.startswith(d + "/") for d in dirs for f in c["files"])
            c["files"] = c["files"][:12]
        fixes = commits_between(repo, last["sha"], recovered["headSha"]) if recovered else []
        for c in fixes:
            c["touches_failing_package"] = any(f.startswith(d + "/") for d in dirs for f in c["files"])
            c["files"] = c["files"][:12]
        all_jobs = {j["name"] for j in json.loads(gh("run", "view", str(first["id"]), "-R", repo, "--json", "jobs"))["jobs"]}
        # the rule-based hint the classifier and the agent must agree with (or say why not)
        if g["infra"] and not g["tests"] and not g["compile_errors"]:
            hint, why = "infra", "infra signature: " + ", ".join(g["infra"])
        elif same_sha_green:
            hint, why = "flaky", f"the same commit {first['sha'][:8]} passed in run {same_sha_green[0]['databaseId']}"
        elif recovered and fixes and not any(c["touches_failing_package"] for c in fixes) and dirs:
            hint, why = "flaky", "it went green again with no commit touching the failing package(s)"
        elif g["tests"] or g["compile_errors"]:
            hint, why = "regression", f"fails deterministically in {len(g['runs'])} run(s) across {len(g['jobs'])} job(s)"
        else:
            hint, why = "unknown", "no test, compiler or infra signature recognised"
        only = sorted(set(g["jobs"]))
        g.update({
            "platform_specific": len(only) < len([j for j in all_jobs if not j.lower().startswith(("lint", "build-ui"))]) and len(only) == 1,
            "category_hint": hint, "hint_why": why, "failing_dirs": dirs,
            "last_green": {"id": last_green["databaseId"], "sha": last_green["headSha"][:10], "url": last_green["url"]} if last_green else None,
            "suspect_commits": suspects[-15:],
            "recovered": {"id": recovered["databaseId"], "sha": recovered["headSha"][:10], "url": recovered["url"]} if recovered else None,
            "fix_commits": fixes[-10:],
            "test_output": {t: d for t, d in list(g["test_output"].items())[:5]},
        })
        out.append(g)

    issues = json.loads(gh("issue", "list", "-R", repo, "--state", "open", "--search", f"{MARK} in:body",
                           "--limit", "200", "--json", "number,title,body,url,labels", ok=True) or "[]")
    by_fp = {}
    for i in issues:
        for line in (i.get("body") or "").splitlines():
            if MARK in line:
                by_fp[line.split(MARK, 1)[1].strip(" ->")] = {"number": i["number"], "url": i["url"], "title": i["title"]}
    for g in out:
        g["existing_issue"] = by_fp.get(g["fingerprint"])
        g["actionable_now"] = g["recovered"] is None or INCLUDE_RECOVERED

    out.sort(key=lambda g: g["runs"][0]["created"])
    max_id = max(r["databaseId"] for r in runs)
    json.dump({"repo": repo, "label": LABEL, "groups": out, "max_run_id": max_id,
               "include_recovered": INCLUDE_RECOVERED}, open(f"{D}/groups.json", "w"), indent=1, default=list)
    print(f"{repo}: {len(runs)} failed run(s) → {len(out)} root-cause group(s) · {len(issues)} open fingerprinted issue(s)")
    for g in out:
        what = ", ".join(g["tests"][:3]) + (f" +{len(g['tests']) - 3}" if len(g["tests"]) > 3 else "") if g["tests"] else g["first_error"][:90]
        rec = f"recovered in run {g['recovered']['id']} ({g['recovered']['sha']})" if g["recovered"] else "STILL FAILING"
        print(f"- {g['fingerprint']} [{g['category_hint']}] {what}\n    runs {[x['id'] for x in g['runs']]} · jobs {g['jobs']} · "
              f"{rec} · {len(g['suspect_commits'])} suspect commit(s)" +
              (f" · existing issue #{g['existing_issue']['number']}" if g["existing_issue"] else ""))
    sys.exit(0)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:
        print(f"collect.py error: {e!r}", file=sys.stderr)
        sys.exit(70)
