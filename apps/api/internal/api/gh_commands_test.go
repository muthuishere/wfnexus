package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubCLI is a fake CLI bound to env var name: it records argv + stdin and
// answers with canned JSON (a URL for the gh write verbs).
func stubCLI(t *testing.T, dir, name, envVar string) func() []map[string]any {
	t.Helper()
	log := filepath.Join(dir, name+"-calls.jsonl")
	script := filepath.Join(dir, name)
	_ = os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
reads_stdin = ("--body-file" in args and args[args.index("--body-file") + 1] == "-") or ("--input" in args) or args[:2] == ["file", "write"]
body = sys.stdin.read() if reads_stdin else ""
open(`+"\""+log+"\""+`, "a").write(json.dumps({"args": args, "body": body}) + "\n")
if args[:2] in (["issue", "create"], ["issue", "comment"], ["pr", "comment"]):
    print("https://github.com/o/r/issues/1")
elif args[:2] == ["file", "read"]:
    print("file contents")
else:
    print(json.dumps([{"number": 7, "title": "x"}]))
`), 0o755)
	t.Setenv(envVar, script)
	return func() []map[string]any {
		raw, _ := os.ReadFile(log)
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var e map[string]any
			if json.Unmarshal([]byte(l), &e) == nil {
				out = append(out, e)
			}
		}
		return out
	}
}

// gh module commands through `wfx module call` against a stub gh. Writes are
// policy-gated (default draft); reads and GET api calls are not.
func TestGhCommandWrappers(t *testing.T) {
	h, _, tmp := aplServer(t, nil)
	calls := stubCLI(t, tmp, "gh", "GH_BIN")
	if w := do(h, "PUT", "/api/modules/gh/profiles/work", "", map[string]any{"account": "me", "default": true, "inputs": map[string]string{"repo": "o/r"}}); w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	last := func(n int, want ...string) {
		t.Helper()
		c := calls()
		if len(c) != n {
			t.Fatalf("want %d gh calls, got %d: %v", n, len(c), c)
		}
		got := argsOf(c[n-1]) + " | " + asString(c[n-1]["body"])
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Fatalf("last gh call %q lacks %q", got, w)
			}
		}
	}
	if st, out := callCmd(t, h, "gh.issue-comment", map[string]any{"number": "12", "body": "looks good"}); st != "done" || !strings.Contains(out, "drafted") || len(calls()) != 0 {
		t.Fatalf("draft: %s %s %v", st, out, calls())
	}
	if st, out := callCmd(t, h, "gh.issue-comment", map[string]any{"number": "12", "body": "looks good", "responsePolicy": "auto"}); st != "done" || !strings.Contains(out, "commented") {
		t.Fatalf("issue-comment: %s %s", st, out)
	}
	last(1, "issue comment 12 -R o/r --body-file -", "| looks good")
	if st := callPaused(t, h, "gh.pr-comment", map[string]any{"number": "3", "body": "ship it", "responsePolicy": "approval"}); st != "awaiting_approval" {
		t.Fatalf("pr-comment approval: %s", st)
	}
	last(1)
	if st, _ := callCmd(t, h, "gh.issue-create", map[string]any{"title": "Bug", "body": "steps", "repo": "x/y", "responsePolicy": "auto"}); st != "done" {
		t.Fatalf("issue-create: %s", st)
	}
	last(2, "issue create -R x/y --title Bug --body-file -", "| steps")
	if st, out := callCmd(t, h, "gh.pr-list", map[string]any{}); st != "done" || !strings.Contains(out, `"number": 7`) {
		t.Fatalf("pr-list: %s %s", st, out)
	}
	last(3, "pr list -R o/r --state open --limit 20 --json")
	if st, _ := callCmd(t, h, "gh.checks", map[string]any{"number": "3"}); st != "done" {
		t.Fatalf("checks: %s", st)
	}
	last(4, "pr checks 3 -R o/r --json name,state,link")
	if st, _ := callCmd(t, h, "gh.release-list", map[string]any{}); st != "done" {
		t.Fatalf("release-list: %s", st)
	}
	last(5, "release list -R o/r --limit 10")
	// gh.api reads (GET only); another method is refused.
	if st, _ := callCmd(t, h, "gh.api", map[string]any{"path": "repos/{repo}/issues"}); st != "done" {
		t.Fatalf("api GET: %s", st)
	}
	last(6, "api -X GET repos/o/r/issues")
	if st, out := callCmd(t, h, "gh.api", map[string]any{"path": "repos/{repo}/labels", "method": "POST"}); st != "failed" && !strings.Contains(out, "gh.api-write") {
		t.Fatalf("api POST must be refused: %s %s", st, out)
	}
	last(6)
	// gh.api-write is the write: it pauses (approved here), then its policy
	// applies: draft writes nothing, auto writes.
	if st, out := callCmd(t, h, "gh.api-write", map[string]any{"path": "repos/{repo}/labels", "method": "POST", "body": `{"name":"bug"}`}); st != "done" || !strings.Contains(out, "drafted") {
		t.Fatalf("api-write draft: %s %s", st, out)
	}
	last(6)
	if st, _ := callCmd(t, h, "gh.api-write", map[string]any{"path": "repos/{repo}/labels", "method": "POST", "body": `{"name":"bug"}`, "responsePolicy": "auto"}); st != "done" {
		t.Fatalf("api-write auto: %s", st)
	}
	last(7, "api -X POST repos/o/r/labels --input -", `| {"name":"bug"}`)
}
