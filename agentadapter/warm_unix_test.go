//go:build !windows

package agentadapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Probing a pid with signal 0 is Unix-only, so this test is too.
// Cancelling the turn's context kills the CLI process mid-turn.
func TestClaudeStreamCancelKillsTheProcess(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	a := &ClaudeStream{Bin: os.Args[0], Env: append(fakeEnv("claude"), "WARM_FAKE_PIDFILE="+pidfile)}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.Execute(ctx, turnText("sleep")); done <- err }()
	var pid int
	for i := 0; i < 200 && pid == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		b, _ := os.ReadFile(pidfile)
		pid, _ = strconv.Atoi(string(b))
	}
	if pid == 0 {
		t.Fatal("fake claude never started its turn")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancel")
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake claude pid %d still alive after cancel", pid)
}
