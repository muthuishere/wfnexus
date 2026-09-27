package devinadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

// fakeModelAgent is an ACP agent that advertises models the way the measured
// agents do (2026-09-27): opencode groups them inside a "model" configOption,
// older agents answer with models{availableModels}. It logs argv and every
// model switch it is asked for, and answers set_config_option with its new
// options — or, when lie is set, with the OLD model still current.
func fakeModelAgent(t *testing.T, shape string, lie bool) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "fake-agent"), filepath.Join(dir, "calls.log")
	script := `#!/usr/bin/env python3
import json,sys,os
d=os.path.dirname(os.path.abspath(__file__))
log=open(d+"/calls.log","a")
log.write("ARGV "+" ".join(sys.argv[1:])+"\n"); log.flush()
SHAPE="` + shape + `"; LIE=` + map[bool]string{true: "True", false: "False"}[lie] + `
cur="opencode/big-pickle"
def opts():
    return [{"id":"model","category":"model","type":"select","currentValue":(("opencode/big-pickle") if LIE else cur),
      "options":[{"group":"opencode","name":"OpenCode Zen","options":[
         {"value":"opencode/big-pickle","name":"Big Pickle"},
         {"value":"opencode/mimo-v2.6-flash-free","name":"MiMo Flash (free)"}]},
       {"group":"openrouter","name":"OpenRouter","options":[{"value":"openrouter/google/gemma-4-31b-it:free","name":"Gemma"}]}]},
      {"id":"mode","category":"mode","type":"select","currentValue":"build","options":[{"value":"build","name":"Build"}]}]
def send(o): sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    m=json.loads(line); mid=m.get("id"); meth=m.get("method"); p=m.get("params",{})
    if meth=="initialize": send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1}})
    elif meth=="session/new":
        r={"sessionId":"s1"}
        if SHAPE=="config": r["configOptions"]=opts()
        if SHAPE=="models": r["models"]={"currentModelId":cur,"availableModels":[{"modelId":"opencode/big-pickle","name":"Big Pickle"},{"modelId":"opencode/mimo-v2.6-flash-free","name":"MiMo"}]}
        send({"jsonrpc":"2.0","id":mid,"result":r})
    elif meth=="session/set_config_option":
        if SHAPE!="config": send({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"Method not found"}}); continue
        cur=p["value"]; log.write("SET config "+cur+"\n"); log.flush()
        send({"jsonrpc":"2.0","id":mid,"result":{"configOptions":opts()}})
    elif meth=="session/set_model":
        cur=p["modelId"]; log.write("SET model "+cur+"\n"); log.flush()
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif meth=="session/prompt":
        log.write("PROMPT on "+cur+"\n"); log.flush()
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"hi from "+cur}}}})
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif mid is not None: send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestACPListsTheModelsAnAgentGroupsInsideConfigOptions(t *testing.T) {
	bin, _ := fakeModelAgent(t, "config", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}})
	defer a.Close()
	ms, cur, err := a.Models(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cur != "opencode/big-pickle" {
		t.Errorf("current = %q", cur)
	}
	var ids, free []string
	for _, m := range ms {
		ids = append(ids, m.ID)
		if m.Free {
			free = append(free, m.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("models = %v, want all three across both groups", ids)
	}
	if strings.Join(free, ",") != "opencode/mimo-v2.6-flash-free,openrouter/google/gemma-4-31b-it:free" {
		t.Errorf("free = %v: only ids that SAY free are free", free)
	}
}

func TestACPReadsTheOlderModelsShapeToo(t *testing.T) {
	bin, _ := fakeModelAgent(t, "models", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}})
	defer a.Close()
	ms, cur, err := a.Models(context.Background(), t.TempDir())
	if err != nil || len(ms) != 2 || cur != "opencode/big-pickle" {
		t.Fatalf("models=%v cur=%q err=%v", ms, cur, err)
	}
}

func TestACPAnAgentThatAdvertisesNoModelsListsNone(t *testing.T) {
	bin, _ := fakeModelAgent(t, "none", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}})
	defer a.Close()
	ms, _, err := a.Models(context.Background(), t.TempDir())
	if err != nil || len(ms) != 0 {
		t.Fatalf("models=%v err=%v", ms, err)
	}
}

func TestACPRunsTheTurnOnTheModelItWasAskedFor(t *testing.T) {
	bin, log := fakeModelAgent(t, "config", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}, Model: "opencode/mimo-v2.6-flash-free"})
	defer a.Close()
	out, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "hello", Workdir: t.TempDir(), Index: 1, Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi from opencode/mimo-v2.6-flash-free" {
		t.Errorf("answered on the wrong model: %q", out)
	}
	l := readLog(t, log)
	// The argv is exactly what was configured: no devin --model flag, no
	// second acp subcommand.
	if !strings.Contains(l, "ARGV acp\n") {
		t.Errorf("argv: %q", strings.SplitN(l, "\n", 2)[0])
	}
	if !strings.Contains(l, "SET config opencode/mimo-v2.6-flash-free") {
		t.Errorf("model was not chosen over the protocol:\n%s", l)
	}
}

func TestACPFallsBackToSetModelWhenConfigOptionsAreNotSupported(t *testing.T) {
	bin, log := fakeModelAgent(t, "models", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}, Model: "opencode/mimo-v2.6-flash-free"})
	defer a.Close()
	if _, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: t.TempDir(), Index: 1, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, log), "SET model opencode/mimo-v2.6-flash-free") {
		t.Error("no session/set_model fallback")
	}
}

func TestACPRefusesAModelTheAgentDoesNotOffer(t *testing.T) {
	bin, log := fakeModelAgent(t, "config", false)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}, Model: "opencode/typo-model"})
	defer a.Close()
	_, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: t.TempDir(), Index: 1, Attempt: 1})
	if err == nil || !strings.Contains(err.Error(), "does not offer model") {
		t.Fatalf("err = %v, want a refusal naming the model", err)
	}
	if strings.Contains(readLog(t, log), "PROMPT") {
		t.Error("a turn ran on the default model after the model was refused")
	}
}

func TestACPAModelTheAgentQuietlyIgnoredIsAnError(t *testing.T) {
	bin, _ := fakeModelAgent(t, "config", true)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{"acp"}, Model: "opencode/mimo-v2.6-flash-free"})
	defer a.Close()
	_, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: t.TempDir(), Index: 1, Attempt: 1})
	if err == nil || !strings.Contains(err.Error(), "the agent is on") {
		t.Fatalf("err = %v, want the mismatch named", err)
	}
}

func TestACPPassesItsEnvToTheAgent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "envagent")
	os.WriteFile(bin, []byte(`#!/usr/bin/env python3
import json,sys,os
for line in sys.stdin:
    m=json.loads(line); mid=m.get("id"); meth=m.get("method")
    if meth=="session/new": r={"sessionId":"s"}
    elif meth=="session/prompt":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":os.environ.get("WFX_T","unset")}}}})+"\n")
        r={"stopReason":"end_turn"}
    else: r={}
    sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":r})+"\n"); sys.stdout.flush()
`), 0o755)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{}, Env: []string{"WFX_T=from-the-step"}})
	defer a.Close()
	out, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: dir, Index: 1, Attempt: 1})
	if err != nil || out != "from-the-step" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestACPAnAgentsErrorCarriesItsOwnReason(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "erragent")
	os.WriteFile(bin, []byte(`#!/usr/bin/env python3
import json,sys
for line in sys.stdin:
    m=json.loads(line); mid=m.get("id"); meth=m.get("method")
    if meth=="session/prompt":
        out={"jsonrpc":"2.0","id":mid,"error":{"code":-32603,"message":"Internal error","data":{"message":"model x is not supported on this plan"}}}
    else:
        out={"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"} if meth=="session/new" else {}}
    sys.stdout.write(json.dumps(out)+"\n"); sys.stdout.flush()
`), 0o755)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{}})
	defer a.Close()
	_, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: dir, Index: 1, Attempt: 1})
	if err == nil || !strings.Contains(err.Error(), "not supported on this plan") {
		t.Fatalf("err = %v: the agent's reason was dropped", err)
	}
}

// A model that never answers must not hold a cancelled step: Close, called
// while the turn is blocked, ends it at once instead of after the timeout.
func TestACPCloseInterruptsATurnTheModelNeverAnswers(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "silent")
	os.WriteFile(bin, []byte(`#!/usr/bin/env python3
import json,sys
for line in sys.stdin:
    m=json.loads(line); mid=m.get("id"); meth=m.get("method")
    if meth=="session/prompt": continue   # never answers
    r={"sessionId":"s"} if meth=="session/new" else {}
    sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":r})+"\n"); sys.stdout.flush()
`), 0o755)
	a := devinadapter.NewACP(devinadapter.ACP{Bin: bin, Argv: []string{}})
	done := make(chan error, 1)
	go func() {
		_, err := a.Execute(context.Background(), devinadapter.Turn{Prompt: "x", Workdir: dir, Index: 1, Attempt: 1})
		done <- err
	}()
	time.Sleep(500 * time.Millisecond)
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a turn with no answer returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not interrupt the blocked turn")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close itself hung")
	}
}
