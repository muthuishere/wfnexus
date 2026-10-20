package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
)

// The FAKE CHANNEL ADAPTER — the test fixture for the command contract
// (docs/channels.md). The test binary re-executes itself as the adapter, so
// the fixture is plain Go and runs wherever the suite runs, no Node needed.
//
//	send     appends the stdin envelope as one JSON line to $FAKE_DIR/sent.jsonl
//	         and prints {"id":"m<N>"}; with $FAKE_DIR/fail present it prints
//	         that file to stderr and exits 3.
//	receive  appends the stdin request to $FAKE_DIR/received.jsonl, then
//	         prints the messages of $FAKE_DIR/inbox.json that come AFTER the
//	         cursor, and the last id as the new cursor.
func fakeAdapterMain() {
	dir := os.Getenv("FAKE_DIR")
	verb := os.Args[len(os.Args)-1]
	in, _ := io.ReadAll(os.Stdin)
	appendLine := func(name string) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			f.Write(append([]byte(strings.TrimSpace(string(in))), '\n'))
			f.Close()
		}
	}
	switch verb {
	case "send":
		if _, err := os.Stat(filepath.Join(dir, "hang")); err == nil {
			time.Sleep(5 * time.Second) // outlives the channel's timeout
		}
		if msg, err := os.ReadFile(filepath.Join(dir, "fail")); err == nil {
			fmt.Fprint(os.Stderr, string(msg))
			os.Exit(3)
		}
		appendLine("sent.jsonl")
		n := len(readLines(filepath.Join(dir, "sent.jsonl")))
		fmt.Printf(`{"id":"m%d"}`, n)
	case "receive":
		appendLine("received.jsonl")
		var req notify.ReceiveRequest
		_ = json.Unmarshal(in, &req)
		var inbox []notify.Message
		raw, _ := os.ReadFile(filepath.Join(dir, "inbox.json"))
		_ = json.Unmarshal(raw, &inbox)
		res := notify.ReceiveResult{Messages: []notify.Message{}, Cursor: req.After}
		seen := req.After == ""
		for _, m := range inbox {
			if seen {
				res.Messages = append(res.Messages, m)
				res.Cursor = m.ID
			}
			if m.ID == req.After {
				seen = true
			}
		}
		json.NewEncoder(os.Stdout).Encode(res)
	default:
		fmt.Fprintln(os.Stderr, "fake adapter: unknown verb "+verb)
		os.Exit(2)
	}
}

func readLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// fakeChannel registers a channel backed by the fake adapter and returns the
// directory it records into.
func fakeChannel(t *testing.T, e *Engine, name string, inbound bool, allow ...string) string {
	t.Helper()
	return fakeChannelWith(t, e, catalog.Channel{Name: name, Inbound: inbound, Allow: allow})
}

// fakeChannelWith registers c (its Kind, Run and Env are filled in) backed by
// the fake adapter.
func fakeChannelWith(t *testing.T, e *Engine, c catalog.Channel) string {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c.Kind, c.Run = "command", strconv.Quote(exe)
	c.Env = map[string]string{"WFX_FAKE_ADAPTER": "1", "FAKE_DIR": dir}
	e.catalog.Channels.Add(c, "test")
	return dir
}

func sentEnvelopes(t *testing.T, dir string) []notify.Envelope {
	t.Helper()
	var out []notify.Envelope
	for _, l := range readLines(filepath.Join(dir, "sent.jsonl")) {
		var e notify.Envelope
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("adapter got a non-JSON envelope: %s", l)
		}
		out = append(out, e)
	}
	return out
}

func writeInbox(t *testing.T, dir string, msgs ...notify.Message) {
	t.Helper()
	raw, _ := json.Marshal(msgs)
	if err := os.WriteFile(filepath.Join(dir, "inbox.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
