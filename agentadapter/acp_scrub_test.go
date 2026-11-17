package agentadapter_test

// Through a real ACP subprocess: what the turn's error shows of the agent (a
// refused tool's title, the stderr of an agent that died) is scrubbed whole
// before it is cut.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentadapter "github.com/muthuishere/wfnexus/agentadapter"
)

var acpSecret = "sk-live-" + strings.Repeat("7f3Qz9XyW2pL5mN8vR4t", 10)

func acpScrub(s string) string { return strings.ReplaceAll(s, acpSecret, "[redacted]") }

func acpPiece(s string) string {
	for n := len(acpSecret); n >= 4; n-- {
		for i := 0; i+n <= len(acpSecret); i++ {
			if strings.Contains(s, acpSecret[i:i+n]) {
				return acpSecret[i : i+n]
			}
		}
	}
	return ""
}

// fakeACPRefusedOrDead writes an agent that, on a prompt, either asks to run a
// tool titled with the contents of <bin>.title and then ends the turn empty,
// or writes <bin>.stderr to stderr and exits.
func fakeACPRefusedOrDead(t *testing.T, title, stderr string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-acp")
	script := `#!/usr/bin/env python3
import json,os,sys
def send(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
me=sys.argv[0]
pending=None
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    m=json.loads(line)
    method=m.get("method"); mid=m.get("id")
    if method=="initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1}})
    elif method=="session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"}})
    elif method=="session/set_mode":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method=="session/prompt":
        if os.path.exists(me+".stderr"):
            sys.stderr.write(open(me+".stderr").read()); sys.stderr.flush(); sys.exit(3)
        pending=mid
        send({"jsonrpc":"2.0","id":9001,"method":"session/request_permission",
              "params":{"sessionId":"s","toolCall":{"toolCallId":"c1","title":open(me+".title").read(),"kind":"read"},"options":[
                  {"optionId":"yes","kind":"allow_once","name":"Allow"},
                  {"optionId":"no","kind":"reject_once","name":"Reject"}]}})
    elif mid==9001 and pending is not None:
        send({"jsonrpc":"2.0","id":pending,"result":{"stopReason":"end_turn"}})
        pending=None
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for ext, body := range map[string]string{".title": title, ".stderr": stderr} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(bin+ext, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func acpTurn(t *testing.T, bin string) error {
	t.Helper()
	acp := agentadapter.NewACP(agentadapter.ACP{Bin: bin})
	defer acp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := acp.Execute(ctx, agentadapter.Turn{Index: 1, Attempt: 1, Prompt: "hi", Workdir: t.TempDir(), Scrub: acpScrub})
	if err == nil {
		t.Fatal("the turn returned no error")
	}
	return err
}

// The title is cut to 80 bytes for the error; the value starts before that
// point and ends after it.
func TestARefusedToolsTitleIsScrubbedBeforeItIsCut(t *testing.T) {
	err := acpTurn(t, fakeACPRefusedOrDead(t, strings.Repeat(".", 60)+acpSecret+" and more", ""))
	if p := acpPiece(err.Error()); p != "" {
		t.Fatalf("the error shows a piece of the value: %q\n%v", p, err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("the refused title is not in the error: %v", err)
	}
}

// The stderr tail is cut to its last 4 KB for the error; the value straddles
// that point.
func TestADeadACPAgentsStderrIsScrubbedBeforeItIsCut(t *testing.T) {
	err := acpTurn(t, fakeACPRefusedOrDead(t, "", acpSecret+strings.Repeat("#", 4096-50)+"\n"))
	if p := acpPiece(err.Error()); p != "" {
		t.Fatalf("the error shows a piece of the value: %q", p)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("the stderr tail is not in the error: %.300v", err)
	}
}
