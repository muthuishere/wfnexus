package agentadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fakes are this test binary itself, re-run with WARM_FAKE set, so they
// work wherever `go test` does (no shell scripts, Windows included).
func init() {
	switch os.Getenv("WARM_FAKE") {
	case "claude":
		fakeClaude()
	case "codex":
		fakeCodex()
	}
}

// fakeClaude answers each stream-json user message with "n=<messages this
// process has seen>: <text>". "die" exits with a stderr line.
func fakeClaude() {
	sc := bufio.NewScanner(os.Stdin)
	n := 0
	for sc.Scan() {
		var m struct {
			Message struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		text := m.Message.Content[0].Text
		if text == "die" {
			fmt.Fprintln(os.Stderr, "fake claude: boom")
			os.Exit(3)
		}
		n++
		fmt.Println(`{"type":"system","subtype":"init"}`)
		b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": fmt.Sprintf("n=%d: %s", n, text)})
		fmt.Println(string(b))
	}
	os.Exit(0)
}

// fakeCodex speaks the app-server subset CodexServer uses. A turn's answer is
// "t=<turns on this thread>: <text>".
func fakeCodex() {
	sc := bufio.NewScanner(os.Stdin)
	turns := map[string]int{}
	threads := 0
	out := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	for sc.Scan() {
		var m struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		switch m.Method {
		case "initialize":
			out(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			threads++
			out(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": fmt.Sprintf("th%d", threads)}}})
		case "turn/start":
			var p struct {
				ThreadID string `json:"threadId"`
				Input    []struct{ Text string }
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.Input[0].Text == "die" {
				fmt.Fprintln(os.Stderr, "fake codex: boom")
				os.Exit(3)
			}
			turns[p.ThreadID]++
			out(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "tu"}}})
			out(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": p.ThreadID}})
			out(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": p.ThreadID, "turn": map[string]any{
				"status": "completed", "items": []map[string]any{{"type": "agentMessage", "text": fmt.Sprintf("t=%d: %s", turns[p.ThreadID], p.Input[0].Text)}}}}})
		}
	}
	os.Exit(0)
}

func fakeEnv(kind string) []string { return []string{"WARM_FAKE=" + kind} }

func turnText(s string) Turn { return Turn{Index: 1, Attempt: 1, Prompt: s} }

func TestClaudeStreamFreshSessionPerTurn(t *testing.T) {
	a := &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude")}
	defer a.Close()
	if err := a.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.Healthy() {
		t.Fatal("not healthy after Warm")
	}
	for i := range 3 {
		out, err := a.Execute(context.Background(), turnText(fmt.Sprint("hi", i)))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("n=1: hi%d", i); out != want {
			t.Fatalf("turn %d = %q, want %q (a fresh session every turn)", i, out, want)
		}
		if !a.Healthy() {
			t.Fatal("the next process was not started after the turn")
		}
	}
}

func TestClaudeStreamReuseKeepsOneConversation(t *testing.T) {
	a := &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude"), Reuse: true}
	defer a.Close()
	for i := 1; i <= 3; i++ {
		out, err := a.Execute(context.Background(), turnText("x"))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("n=%d: x", i); out != want {
			t.Fatalf("got %q, want %q", out, want)
		}
	}
}

func TestClaudeStreamRestartsAKilledProcess(t *testing.T) {
	a := &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude"), Reuse: true}
	defer a.Close()
	if _, err := a.Execute(context.Background(), turnText("x")); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	p := a.cur
	a.mu.Unlock()
	_ = p.cmd.Process.Kill()
	<-p.done
	if a.Healthy() {
		t.Fatal("Healthy after the process was killed")
	}
	out, err := a.Execute(context.Background(), turnText("again"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "n=1: again" {
		t.Fatalf("got %q, want a new process's first answer", out)
	}
}

func TestClaudeStreamErrorCarriesStderrAndRecovers(t *testing.T) {
	a := &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude")}
	defer a.Close()
	_, err := a.Execute(context.Background(), turnText("die"))
	if err == nil || !strings.Contains(err.Error(), "fake claude: boom") {
		t.Fatalf("err = %v, want the stderr tail in it", err)
	}
	out, err := a.Execute(context.Background(), turnText("ok"))
	if err != nil || out != "n=1: ok" {
		t.Fatalf("after a crash: %q, %v", out, err)
	}
}

func TestClaudeStreamClosed(t *testing.T) {
	a := &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude")}
	_ = a.Close()
	if _, err := a.Execute(context.Background(), turnText("x")); err != ErrClosed {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestCodexServerFreshThreadPerTurn(t *testing.T) {
	a := &CodexServer{Bin: os.Args[0], Env: fakeEnv("codex")}
	defer a.Close()
	if err := a.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		out, err := a.Execute(context.Background(), turnText(fmt.Sprint("q", i)))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("t=1: q%d", i); out != want {
			t.Fatalf("got %q, want %q (a fresh thread every turn)", out, want)
		}
	}
}

func TestCodexServerReuseKeepsOneThread(t *testing.T) {
	a := &CodexServer{Bin: os.Args[0], Env: fakeEnv("codex"), Reuse: true}
	defer a.Close()
	for i := 1; i <= 3; i++ {
		out, err := a.Execute(context.Background(), turnText("q"))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("t=%d: q", i); out != want {
			t.Fatalf("got %q, want %q", out, want)
		}
	}
}

func TestCodexServerRestartsAfterACrash(t *testing.T) {
	a := &CodexServer{Bin: os.Args[0], Env: fakeEnv("codex")}
	defer a.Close()
	_, err := a.Execute(context.Background(), turnText("die"))
	if err == nil || !strings.Contains(err.Error(), "fake codex: boom") {
		t.Fatalf("err = %v, want the stderr tail in it", err)
	}
	out, err := a.Execute(context.Background(), turnText("ok"))
	if err != nil || out != "t=1: ok" {
		t.Fatalf("after a crash: %q, %v", out, err)
	}
	if !a.Healthy() {
		t.Fatal("not healthy after the restart")
	}
}

func TestPoolSharesAgentsAcrossCallers(t *testing.T) {
	p := NewPool(2, func() WarmAgent { return &ClaudeStream{Bin: os.Args[0], Env: fakeEnv("claude")} })
	defer p.Close()
	if err := p.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := p.Execute(ctx, turnText(fmt.Sprint("c", i)))
			if err == nil && out != fmt.Sprintf("n=1: c%d", i) {
				err = fmt.Errorf("caller %d got %q", i, out)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackoffGrowsAndResets(t *testing.T) {
	var b backoff
	b.failed()
	first := time.Until(b.until)
	b.failed()
	b.failed()
	if time.Until(b.until) <= first {
		t.Fatal("backoff did not grow")
	}
	for range 20 {
		b.failed()
	}
	if time.Until(b.until) > 10*time.Second {
		t.Fatal("backoff above its 10 s cap")
	}
	b.ok()
	if err := b.wait(context.Background()); err != nil || b.fails != 0 {
		t.Fatal("ok did not reset the backoff")
	}
}
