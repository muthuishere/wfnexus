package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReadableCommandDropsTheEnvPreamble(t *testing.T) {
	cases := map[string]string{
		`. '/Users/me/.wfx/runs/r1/secrets/ask-local.env' && export WFX_API='http://127.0.0.1:8090' WFX_RUN='r1' GH="${GH_TOKEN}" && ls -la`: "ls -la",
		`export A='it'\''s' && git status && echo done`: "git status && echo done",
		`WFX_RUN=r1 WFX_STEP='s' go test ./...`:         "go test ./...",
		`cat /tmp/x/secrets/step.env`:                   "cat <secrets>",
		`echo plain`:                                    "echo plain",
	}
	for in, want := range cases {
		if got := readableCommand(in); got != want {
			t.Errorf("readableCommand(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// Golden: the quiet view of a whole run, event by event.
func TestQuietViewGolden(t *testing.T) {
	cost := 0.0312
	q := &quietView{runID: "r1", base: "http://h", lookup: func(id string) (stepSummary, bool) {
		return stepSummary{Turns: 7, Elapsed: 64 * time.Second, Cost: &cost,
			Output: json.RawMessage(`{"answer":"the cache key ignores the tenant & region"}`)}, true
	}}
	type ev struct {
		step, kind, payload string
	}
	events := []ev{
		{"ask-local", "run.status", `{"status":"running"}`},
		{"ask-local", "step.status", `{"status":"running"}`},
		{"ask-local", "llm", `{"turn":1,"text":"Let me look around the repository first."}`},
		{"ask-local", "tool_call", `{"name":"bash","args":{"command":". '/Users/me/runs/r1/secrets/ask-local.env' && export WFX_API='http://127.0.0.1:8090' WFX_RUN='r1' && ls -la && cat go.mod"}}`},
		{"ask-local", "tool_result", `{"name":"bash","isError":true,"output":"/Users/me/repo\ntotal 0\ndrwxr-xr-x@ 3 me staff"}`},
		{"ask-local", "tool_call", `{"name":"read","args":{"path":"internal/cache/key.go"}}`},
		{"ask-local", "tool_result", `{"name":"read","isError":false,"output":"package cache\n..."}`},
		{"ask-local", "metric", `{"Event":"llm"}`},
		{"ask-local", "tool_call", `{"name":"submit_output","args":{"answer":"x"}}`},
		{"ask-local", "tool_result", `{"name":"submit_output","isError":true,"output":"missing field"}`},
		{"ask-local", "tool_call", `{"name":"submit_output","args":{"answer":"x"}}`},
		{"ask-local", "artifact", `{"id":"a1","name":"answer.md","sizeBytes":2048}`},
		{"ask-local", "step.status", `{"status":"done"}`},
		{"ask-local", "run.status", `{"status":"done"}`},
	}
	var got []string
	for _, e := range events {
		var p map[string]any
		if err := json.Unmarshal([]byte(e.payload), &p); err != nil {
			t.Fatal(err)
		}
		got = append(got, q.line(e.step, e.kind, p)...)
	}
	want := `▸ ask-local
  · bash  ls -la && cat go.mod
    ✗ /Users/me/repo
  · read  internal/cache/key.go
  · submit
    ✗ output rejected — the step retries
  · submit
  · artifact answer.md (2.0 KB)  http://h/api/runs/r1/artifacts/a1
✓ ask-local  7 turns · 1m4s · $0.03

result: the cache key ignores the tenant & region
artifacts:
  answer.md (2.0 KB)  http://h/api/runs/r1/artifacts/a1
run done   (wfx show r1)`
	if g := strings.Join(got, "\n"); g != want {
		t.Fatalf("quiet view drifted\n--- got\n%s\n--- want\n%s", g, want)
	}
}
