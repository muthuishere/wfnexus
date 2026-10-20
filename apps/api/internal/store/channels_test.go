package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"
)

// A re-delivered message is stored once, and the second insert says so —
// that is what keeps a reply from resolving or triggering twice.
func TestChannelMessagesAreStoredOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
		m := &ChannelMessage{Channel: n("ops"), MsgID: n("M1"), Sender: "alice", Text: "#ab12 yes", SentAt: &at}
		if ok, err := st.InsertChannelMessage(ctx, m); err != nil || !ok {
			t.Fatalf("first insert ok=%v err=%v", ok, err)
		}
		if ok, err := st.InsertChannelMessage(ctx, m); err != nil || ok {
			t.Fatalf("second insert ok=%v err=%v — a re-delivery must be a no-op", ok, err)
		}
		if err := st.SetChannelMessageOutcome(ctx, n("ops"), n("M1"), n("run-1"), "resolved"); err != nil {
			t.Fatal(err)
		}
		got, err := st.ListChannelMessages(ctx, ChannelMessageFilter{Channel: n("ops"), All: true})
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v err %v", got, err)
		}
		if got[0].Outcome != "resolved" || got[0].RunID != n("run-1") || got[0].Sender != "alice" {
			t.Fatalf("row %+v", got[0])
		}
		if other, _ := st.ListChannelMessages(ctx, ChannelMessageFilter{Channel: n("mail"), All: true}); len(other) != 0 {
			t.Fatalf("channel filter leaked: %v", other)
		}
	})
}

func TestDedupClaimHoldsForTheWindow(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		if ok, _, err := st.DedupClaim(ctx, n("ops"), n("k"), time.Hour); err != nil || !ok {
			t.Fatalf("first claim ok=%v err=%v", ok, err)
		}
		if ok, last, err := st.DedupClaim(ctx, n("ops"), n("k"), time.Hour); err != nil || ok || last.IsZero() {
			t.Fatalf("second claim inside the window ok=%v last=%v err=%v", ok, last, err)
		}
		if ok, _, _ := st.DedupClaim(ctx, n("other"), n("k"), time.Hour); !ok {
			t.Fatal("a key is per channel")
		}
		time.Sleep(5 * time.Millisecond) // SQLite keeps milliseconds
		if ok, _, _ := st.DedupClaim(ctx, n("ops"), n("k"), time.Millisecond); !ok {
			t.Fatal("outside the window the key must be claimable again")
		}
		if ok, _, _ := st.DedupClaim(ctx, n("ops"), n("once"), 0); !ok {
			t.Fatal("first once-ever claim")
		}
		if ok, _, _ := st.DedupClaim(ctx, n("ops"), n("once"), 0); ok {
			t.Fatal("window 0 means once, ever")
		}
		if err := st.DedupRelease(ctx, n("ops"), n("k")); err != nil {
			t.Fatal(err)
		}
		if ok, _, _ := st.DedupClaim(ctx, n("ops"), n("k"), time.Hour); !ok {
			t.Fatal("a released claim must be claimable again")
		}
	})
}

// The claim is ONE statement: twenty runs racing for the same key produce
// exactly one send.
func TestDedupClaimIsAtomicUnderConcurrency(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, _, err := st.DedupClaim(ctx, n("ops"), n("race"), time.Hour)
				if err != nil {
					t.Error(err)
				}
				if ok {
					mu.Lock()
					won++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Fatalf("%d claims won, want exactly 1", won)
		}
	})
}

// A code is per channel, never overwritten on collision, and retired with its
// pause.
func TestPauseCodesAreScopedAndRetired(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		exp := time.Now().Add(time.Hour)
		c := PauseCode{Channel: n("ops"), Code: n("ab12cd"), RunID: n("r"), StepID: n("s"), Kind: "approval", ExpiresAt: exp}
		if ok, err := st.InsertPauseCode(ctx, c); err != nil || !ok {
			t.Fatalf("insert ok=%v err=%v", ok, err)
		}
		other := c
		other.RunID = n("r2")
		if ok, _ := st.InsertPauseCode(ctx, other); ok {
			t.Fatal("a colliding code must not be re-pointed")
		}
		got, err := st.PauseByCode(ctx, n("ops"), n("ab12cd"))
		if err != nil || got == nil || got.RunID != n("r") || !got.Live(time.Now()) {
			t.Fatalf("got %+v err %v", got, err)
		}
		if got, _ := st.PauseByCode(ctx, n("mail"), n("ab12cd")); got != nil {
			t.Fatal("a code must not match on another channel")
		}
		if err := st.RetirePauseCodes(ctx, n("r"), n("s")); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.PauseByCode(ctx, n("ops"), n("ab12cd")); got == nil || got.Live(time.Now()) {
			t.Fatalf("a retired code must not be live: %+v", got)
		}
		if err := st.PruneChannels(ctx, time.Now().Add(time.Minute), time.Now().Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.PauseByCode(ctx, n("ops"), n("ab12cd")); got != nil {
			t.Fatal("retention must drop a retired code")
		}
	})
}

// A stored message is pending until it is handled.
func TestPendingMessagesUntilHandled(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		_, _ = st.InsertChannelMessage(ctx, &ChannelMessage{Channel: n("p"), MsgID: n("1"), Text: "x"})
		pend, err := st.PendingChannelMessages(ctx, n("p"))
		if err != nil || len(pend) != 1 || pend[0].Handled {
			t.Fatalf("pending %v err %v", pend, err)
		}
		_ = st.SetChannelMessageOutcome(ctx, n("p"), n("1"), "", "")
		if pend, _ := st.PendingChannelMessages(ctx, n("p")); len(pend) != 0 {
			t.Fatal("a handled message is not pending")
		}
	})
}

// namer makes one test invocation's keys unique: the postgres database is
// shared by the whole package run, and `go test -count=2` must not meet the
// first pass's rows.
func namer() func(string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	return func(s string) string { return s + "-" + sfx }
}

// A message starts a workflow once: the second claim for the same (channel,
// message, workflow) loses, and a release gives it back.
func TestMessageRunClaimIsOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		if ok, err := st.ClaimMessageRun(ctx, n("ops"), n("m"), "wf"); err != nil || !ok {
			t.Fatalf("first claim ok=%v err=%v", ok, err)
		}
		if ok, _ := st.ClaimMessageRun(ctx, n("ops"), n("m"), "wf"); ok {
			t.Fatal("a second claim for the same message and workflow must lose")
		}
		if ok, _ := st.ClaimMessageRun(ctx, n("ops"), n("m"), "other-wf"); !ok {
			t.Fatal("another workflow has its own claim")
		}
		_ = st.SetMessageRun(ctx, n("ops"), n("m"), "wf", "run-x")
		_ = st.ReleaseMessageRun(ctx, n("ops"), n("m"), "wf")
		if ok, _ := st.ClaimMessageRun(ctx, n("ops"), n("m"), "wf"); !ok {
			t.Fatal("a released claim is claimable again")
		}
	})
}

// A message is visible in a project only when it answered or started a run
// there; a message linked to no run is visible only to an unscoped "all".
func TestChannelMessagesAreProjectScoped(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		n := namer()
		ctx := context.Background()
		ch := n("scoped")
		runA, err := st.CreateRun(ctx, n("projA"), "wf", []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		runB, _ := st.CreateRun(ctx, n("projB"), "wf", []byte("{}"))
		for _, id := range []string{"answered", "started", "unmatched"} {
			if _, err := st.InsertChannelMessage(ctx, &ChannelMessage{Channel: ch, MsgID: id, Text: id}); err != nil {
				t.Fatal(err)
			}
		}
		_ = st.SetChannelMessageOutcome(ctx, ch, "answered", runA.ID.String(), "resolved")
		_, _ = st.ClaimMessageRun(ctx, ch, "started", "wf")
		_ = st.SetMessageRun(ctx, ch, "started", "wf", runB.ID.String())
		_ = st.SetChannelMessageOutcome(ctx, ch, "started", "", "triggered")

		ids := func(f ChannelMessageFilter) string {
			f.Channel = ch
			rows, err := st.ListChannelMessages(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, m := range rows {
				out = append(out, m.MsgID)
			}
			return strings.Join(out, ",")
		}
		for _, tc := range []struct {
			f    ChannelMessageFilter
			want string
		}{
			{ChannelMessageFilter{Project: n("projA")}, "answered"},
			{ChannelMessageFilter{Project: n("projB")}, "started"},
			{ChannelMessageFilter{Project: n("projA"), All: true}, "answered"}, // a scope wins over All
			{ChannelMessageFilter{}, "started,answered"},
			{ChannelMessageFilter{All: true}, "unmatched,started,answered"},
			{ChannelMessageFilter{RunID: runB.ID.String()}, "started"},
		} {
			if got := ids(tc.f); got != tc.want {
				t.Errorf("%+v: got %q, want %q", tc.f, got, tc.want)
			}
		}
	})
}
