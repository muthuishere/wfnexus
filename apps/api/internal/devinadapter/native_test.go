package devinadapter_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolnexus "github.com/muthuishere/toolnexus/golang"
	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

// An agent refused its own tools usually goes silent. What it meant to do is
// known — opencode announces each call's arguments — so the platform runs the
// equivalent of its OWN tools instead, under the step's allowlist and
// guardrails. Only tools the step offers are used; an edit, a fetch or a
// sub-task has no safe equivalent and stays refused.
func TestRefusedNativeCallsBecomeThePlatformsOwnCalls(t *testing.T) {
	native := []devinadapter.NativeCall{
		{ID: "1", Tool: "read", Kind: "read", Input: map[string]any{"filePath": "/w/facts.txt"}},
		{ID: "2", Tool: "grep", Kind: "search", Input: map[string]any{"pattern": "answer", "include": "*.py"}},
		{ID: "3", Tool: "glob", Kind: "search", Input: map[string]any{"pattern": "**/*.md", "path": "/w"}},
		{ID: "4", Tool: "bash", Kind: "execute", Input: map[string]any{"command": "ls"}},
		{ID: "5", Tool: "edit", Kind: "edit", Input: map[string]any{"filePath": "/w/x", "newString": "y"}},
		{ID: "6", Tool: "list", Kind: "search", Input: map[string]any{"path": "/w"}},
	}
	got := devinadapter.TranslateNativeCalls(native, map[string]bool{"read": true, "grep": true, "glob": true})
	var names []string
	for _, c := range got {
		b, _ := json.Marshal(c.Arguments)
		names = append(names, c.Name+string(b))
	}
	want := []string{
		`read{"path":"/w/facts.txt"}`,
		`grep{"include":"*.py","pattern":"answer"}`,
		`glob{"path":"/w","pattern":"**/*.md"}`,
		`glob{"path":"/w","pattern":"*"}`,
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("got  %v\nwant %v (bash is not offered here; edit never has an equivalent)", names, want)
	}
}

type readArgs struct {
	Path string `json:"path"`
}

// End to end through a real subprocess: the agent announces its own read (the
// path arrives on an update AFTER the permission request), is refused and
// falls silent. The step's read tool runs on that path, and the agent answers
// on the next turn with the result in hand.
func TestASilentTurnAfterARefusedReadRunsThePlatformsRead(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-native")
	script := `#!/usr/bin/env python3
import json,sys
def send(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
n=0; pending=None
for line in sys.stdin:
    m=json.loads(line); method=m.get("method"); mid=m.get("id")
    if method=="initialize": send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1}})
    elif method=="session/new": send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"}})
    elif method=="session/set_mode": send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method=="session/prompt":
        n+=1
        if n==1:
            pending=mid
            send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"read","kind":"read","rawInput":{}}}})
            send({"jsonrpc":"2.0","id":9001,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"c1","title":"read","kind":"read","rawInput":{}},"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}})
        else:
            text=m["params"]["prompt"][0]["text"]
            ok="42 from the platform" in text
            reply={"role":"assistant","content":"saw it" if ok else "did not see the tool result","tool_calls":[]}
            send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"<openai_response>"+json.dumps(reply)+"</openai_response>"}}}})
            send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif mid==9001 and pending is not None:
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","kind":"read","rawInput":{"filePath":"FACTS"}}}})
        send({"jsonrpc":"2.0","id":pending,"result":{"stopReason":"end_turn"}}); pending=None
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var readPath string
	readTool := toolnexus.NativeToolReflect[readArgs]("read", "read a file",
		func(_ context.Context, in readArgs) (string, error) {
			readPath = in.Path
			return "the answer is 42 from the platform", nil
		})
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false, ExtraTools: []toolnexus.Tool{readTool}})
	if err != nil {
		t.Fatal(err)
	}
	acp := devinadapter.NewACP(devinadapter.ACP{Bin: bin})
	defer acp.Close()
	a := devinadapter.New(devinadapter.Options{Agent: acp, Workdir: dir})
	res, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "what is the answer in FACTS?", tk)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if readPath != "FACTS" {
		t.Fatalf("the platform's read ran on %q, want the path the agent announced (FACTS)", readPath)
	}
	if !strings.Contains(res.Text, "saw it") {
		t.Fatalf("the tool result did not reach the agent's next turn: %q", res.Text)
	}
}
