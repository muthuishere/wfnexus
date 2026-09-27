package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// `?once=1` is the backlog as a finite response. Without it the endpoint is a
// live stream that never ends, and an agent that curled it to read a failed
// run's history hung for twelve minutes.
func TestEventsOnceReturnsTheBacklogAndEnds(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()
	run, err := st.CreateRun(ctx, "local", "wf", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first", "second"} {
		if _, err := st.AppendEvent(ctx, run.ID, "s", "log", map[string]any{"text": msg}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan string, 1)
	go func() { done <- do(h, "GET", "/api/runs/"+run.ID.String()+"/events?after=0&once=1", "", nil).Body.String() }()
	select {
	case body := <-done:
		if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
			t.Fatalf("backlog missing from the response: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("once=1 did not end the response")
	}
}
