#!/usr/bin/env python3
"""gh_cmd.py — the gh module's thin command wrappers (#109).

    gh_cmd.py plan <command>   a writing command: apply the response policy
    gh_cmd.py act  <command>   do it through `gh` and print ONE JSON result

Inputs arrive as files in.<name> in the step's workspace. Every call is the
GitHub CLI ($GH_BIN, default `gh`) with its own login. The repository is the
`repo` input, else the profile's `repo` input.

Writes (issue-create, issue-comment, pr-comment, api-write) are policy-gated:
plan exits 0 auto, 10 approval, 11 draft, 12 none, 2 bad input; draft (the
default) and none touch nothing. `api` reads only (GET); `api-write` needs a
method other than GET.
"""
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request

# command → required inputs
COMMANDS = {
    "issue-create": ["title"],
    "issue-comment": ["number", "body"],
    "pr-comment": ["number", "body"],
    "pr-list": [],
    "checks": ["number"],
    "release-list": [],
    "api": ["path"],
    "api-write": ["path", "method"],
}
WRITES = {"issue-create", "issue-comment", "pr-comment", "api-write"}
POLICY = {"auto": 0, "approval": 10, "draft": 11, "none": 12}


def inp(name, default=""):
    try:
        return open("in." + name).read().rstrip("\n")
    except FileNotFoundError:
        return default


def out(res, code=0):
    print(json.dumps(res))
    sys.exit(code)


def writes(cmd):
    return cmd in WRITES


def method_problem(cmd):
    """api reads (GET only); api-write writes (any method but GET)."""
    m = inp("method", "GET").strip().upper() or "GET"
    if cmd == "api" and m != "GET":
        return "gh.api reads only (GET); use gh.api-write for " + m
    if cmd == "api-write" and m == "GET":
        return "gh.api-write needs a method other than GET; use gh.api to read"
    return ""


def profile():
    api, tok = os.environ.get("WFX_API", "").rstrip("/"), os.environ.get("WFX_MODULE_TOKEN", "")
    if not api or not tok:
        return {}
    url = api + "/api/module-self/" + os.environ.get("WFX_MODULE", "gh") + "/profile?" + \
        urllib.parse.urlencode({"name": inp("profile")})
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers={"Authorization": "Bearer " + tok}), timeout=20) as r:
            return json.loads(r.read())
    except Exception:
        return {}


def repo():
    r = inp("repo").strip()
    return r or ((profile().get("inputs") or {}).get("repo") or "").strip()


def gh(*args, body=None, parse=True):
    cmd = [os.environ.get("GH_BIN") or "gh", *args]
    r = subprocess.run(cmd, input=body, capture_output=True, text=True, timeout=120)
    if r.returncode != 0:
        out({"status": "failed", "error": (r.stderr or r.stdout).strip()[-500:], "command": list(args[:3])}, 1)
    if not parse:
        return r.stdout.strip()
    try:
        return json.loads(r.stdout) if r.stdout.strip() else {}
    except ValueError:
        return {"raw": r.stdout[-4000:]}


def plan(cmd):
    bad = [n + " is required" for n in COMMANDS[cmd] if not inp(n).strip()]
    if method_problem(cmd):
        bad.append(method_problem(cmd))
    pol = inp("responsePolicy", "draft").strip() or "draft"
    if pol not in POLICY:
        bad.append("responsePolicy is draft, approval, auto or none")
    if bad:
        print("invalid: " + "; ".join(bad))
        sys.exit(2)
    if not writes(cmd):
        print(json.dumps({"command": cmd, "policy": "read"}))
        sys.exit(0)
    print(json.dumps({"command": cmd, "policy": pol}))
    sys.exit(POLICY[pol])


def act(cmd):
    bad = [n + " is required" for n in COMMANDS[cmd] if not inp(n).strip()]
    if method_problem(cmd):
        bad.append(method_problem(cmd))
    if bad:
        out({"status": "failed", "error": "; ".join(bad)}, 2)
    pol = inp("responsePolicy", "draft").strip() or "draft"
    if writes(cmd) and pol in ("draft", "none"):
        out({"status": "drafted" if pol == "draft" else "skipped", "command": cmd,
             "request": {n: inp(n) for n in COMMANDS[cmd] + ["repo", "body", "method"] if inp(n)}})
    r = repo()
    if cmd not in ("api", "api-write") and not r:
        out({"status": "failed", "error": "no repo: pass repo=owner/name, or set the profile's repo"}, 2)
    globals()["do_" + cmd.replace("-", "_")](r)


def do_issue_create(r):
    url = gh("issue", "create", "-R", r, "--title", inp("title"), "--body-file", "-", body=inp("body"), parse=False)
    out({"status": "created", "command": "issue-create", "repo": r, "url": url.splitlines()[-1] if url else ""})


def do_issue_comment(r):
    url = gh("issue", "comment", inp("number"), "-R", r, "--body-file", "-", body=inp("body"), parse=False)
    out({"status": "commented", "command": "issue-comment", "repo": r, "url": url})


def do_pr_comment(r):
    url = gh("pr", "comment", inp("number"), "-R", r, "--body-file", "-", body=inp("body"), parse=False)
    out({"status": "commented", "command": "pr-comment", "repo": r, "url": url})


def do_pr_list(r):
    prs = gh("pr", "list", "-R", r, "--state", inp("state", "open") or "open", "--limit", inp("limit", "20") or "20",
             "--json", "number,title,author,url,state,headRefName")
    out({"status": "ok", "command": "pr-list", "repo": r, "prs": prs})


def do_checks(r):
    checks = gh("pr", "checks", inp("number"), "-R", r, "--json", "name,state,link")
    out({"status": "ok", "command": "checks", "repo": r, "checks": checks})


def do_release_list(r):
    rel = gh("release", "list", "-R", r, "--limit", inp("limit", "10") or "10", "--json", "tagName,name,publishedAt,isLatest")
    out({"status": "ok", "command": "release-list", "repo": r, "releases": rel})


def do_api(r):
    path = inp("path").replace("{repo}", r)
    method = (inp("method", "GET").strip().upper() or "GET")
    args = ["api", "-X", method, path]
    body = inp("body")
    if body:
        args += ["--input", "-"]
    res = gh(*args, body=body or None)
    out({"status": "ok", "command": "api", "method": method, "path": path, "result": res})


def do_api_write(r):
    do_api(r)


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("plan", "act") or sys.argv[2] not in COMMANDS:
        print("usage: gh_cmd.py plan|act <%s>" % "|".join(COMMANDS))
        sys.exit(2)
    (plan if sys.argv[1] == "plan" else act)(sys.argv[2])
