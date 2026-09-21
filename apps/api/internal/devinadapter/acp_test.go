//go:build toolnexus_inprocess

// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter_test

// The ACP backend against a REAL subprocess speaking real JSON-RPC — a fake
// agent, but a genuine pipe, so the framing, demultiplexing, session handling
// and process lifetime are all exercised. The live benchmark is in live_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	devinadapter "github.com/muthuishere/bug-fixer-platform/apps/api/internal/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// fakeACP writes a python ACP server that replays canned replies and records
// every prompt it was sent.
func fakeACP(t *testing.T, replies []string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-devin")
	log = filepath.Join(dir, "prompts.log")

	repl, err := os.Create(filepath.Join(dir, "replies.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// One reply per line; \n inside a reply is escaped.
	for _, r := range replies {
		repl.WriteString(strings.ReplaceAll(r, "\n", "\\n") + "\n")
	}
	repl.Close()

	script := `#!/usr/bin/env python3
import json,sys,os
d=os.path.dirname(os.path.abspath(__file__))
replies=[l.rstrip("\n").replace("\\n","\n") for l in open(d+"/replies.txt")]
log=open(d+"/prompts.log","a")
# argv must carry the acp subcommand; anything before it is a global flag.
log.write("ARGV "+" ".join(sys.argv[1:])+"\n"); log.flush()
n=0
def send(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    m=json.loads(line)
    method=m.get("method"); mid=m.get("id")
    if method=="initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1}})
    elif method=="session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess-1"}})
    elif method=="session/set_mode":
        log.write("MODE "+m["params"]["modeId"]+"\n"); log.flush()
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method=="session/prompt":
        text=m["params"]["prompt"][0]["text"]
        log.write("PROMPT "+json.dumps(text)+"\n"); log.flush()
        reply=replies[n] if n<len(replies) else replies[-1]
        n+=1
        # stream it in two chunks, with noise the client must ignore
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"thinking out loud, must not appear"}}}})
        half=len(reply)//2
        for part in (reply[:half], reply[half:]):
            send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"text":part}}}})
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake agent wrote no log: %v", err)
	}
	return string(b)
}

// One process, one session, many turns — the whole point of the backend.
func TestACPReusesOneProcessAcrossTurns(t *testing.T) {
	bin, log := fakeACP(t, []string{
		toolCall("submit_answer", Triage{
			Summary: "s", RootCause: "r", Severity: "high",
			Files: []string{"f.go"}, Fix: []string{"x"}, Confidence: 0.9,
		}),
		answer("Filed."),
	})

	acp := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Model: "SWE-1.6 Slow"})
	defer acp.Close()

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := devinadapter.New(devinadapter.Options{Agent: acp, Workdir: t.TempDir()})
	res, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).
		Run(context.Background(), "triage this", tk)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s", res.Status)
	}

	out := readLog(t, log)
	// Two turns, ONE process: the log is append-only per process, so a second
	// ARGV line would mean the agent was restarted.
	if n := strings.Count(out, "ARGV "); n != 1 {
		t.Errorf("process started %d times, want 1", n)
	}
	if n := strings.Count(out, "PROMPT "); n != 2 {
		t.Errorf("prompts = %d, want 2", n)
	}
	// The model is a GLOBAL flag, before the subcommand.
	if !strings.Contains(out, "ARGV --model SWE-1.6 Slow acp") {
		t.Errorf("argv wrong: %q", strings.SplitN(out, "\n", 2)[0])
	}
	if !strings.Contains(out, "MODE bypass") {
		t.Error("session mode was not set to bypass")
	}
	// Turn 2 must announce that it supersedes turn 1, or a stateful session
	// can answer the stale request.
	if !strings.Contains(out, "SUPERSEDES") {
		t.Error("the follow-up prompt did not supersede the earlier one")
	}
}

// Thought chunks and tool narration are not the reply.
func TestACPIgnoresNonMessageUpdates(t *testing.T) {
	bin, _ := fakeACP(t, []string{answer("just the answer")})
	acp := devinadapter.NewACP(devinadapter.ACP{Bin: bin})
	defer acp.Close()

	out, err := acp.Execute(context.Background(), devinadapter.Turn{
		Index: 1, Attempt: 1, Prompt: "hi", Workdir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "thinking out loud") {
		t.Errorf("a thought chunk leaked into the reply: %q", out)
	}
	res, err := devinadapter.ParseReply(out)
	if err != nil {
		t.Fatalf("streamed reply did not reassemble into valid json: %v\n%s", err, out)
	}
	if res.Content != "just the answer" {
		t.Errorf("content = %q", res.Content)
	}
}

// A dead agent must fail the turn, not hang until the timeout.
func TestACPProcessDeathSurfaces(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instant-death")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	acp := devinadapter.NewACP(devinadapter.ACP{Bin: bin})
	defer acp.Close()

	_, err := acp.Execute(context.Background(), devinadapter.Turn{Index: 1, Prompt: "hi", Workdir: dir})
	if err == nil {
		t.Fatal("expected an error from a dead agent")
	}
	if !strings.Contains(err.Error(), "devin-acp") {
		t.Errorf("error should name the backend: %v", err)
	}
}

// Close is idempotent and safe before any start.
func TestACPCloseIsSafe(t *testing.T) {
	acp := devinadapter.NewACP(devinadapter.ACP{Bin: "does-not-exist"})
	if err := acp.Close(); err != nil {
		t.Errorf("close before start: %v", err)
	}
	if err := acp.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}
