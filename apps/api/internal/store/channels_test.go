package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A re-delivered message is stored once, and the second insert says so —
// that is what keeps a reply from resolving or triggering twice.
func TestChannelMessagesAreStoredOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
		m := &ChannelMessage{Channel: "ops", MsgID: "M1", Sender: "alice", Text: "#ab12 yes", SentAt: &at}
		if ok, err := st.InsertChannelMessage(ctx, m); err != nil || !ok {
			t.Fatalf("first insert ok=%v err=%v", ok, err)
		}
		if ok, err := st.InsertChannelMessage(ctx, m); err != nil || ok {
			t.Fatalf("second insert ok=%v err=%v — a re-delivery must be a no-op", ok, err)
		}
		if err := st.SetChannelMessageOutcome(ctx, "ops", "M1", "run-1", "resolved"); err != nil {
			t.Fatal(err)
		}
		got, err := st.ListChannelMessages(ctx, "ops", "", 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v err %v", got, err)
		}
		if got[0].Outcome != "resolved" || got[0].RunID != "run-1" || got[0].Sender != "alice" {
			t.Fatalf("row %+v", got[0])
		}
		if other, _ := st.ListChannelMessages(ctx, "mail", "", 10); len(other) != 0 {
			t.Fatalf("channel filter leaked: %v", other)
		}
	})
}

func TestDedupClaimHoldsForTheWindow(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		if ok, _, err := st.DedupClaim(ctx, "ops", "k", time.Hour); err != nil || !ok {
			t.Fatalf("first claim ok=%v err=%v", ok, err)
		}
		if ok, last, err := st.DedupClaim(ctx, "ops", "k", time.Hour); err != nil || ok || last.IsZero() {
			t.Fatalf("second claim inside the window ok=%v last=%v err=%v", ok, last, err)
		}
		if ok, _, _ := st.DedupClaim(ctx, "other", "k", time.Hour); !ok {
			t.Fatal("a key is per channel")
		}
		time.Sleep(5 * time.Millisecond) // SQLite keeps milliseconds
		if ok, _, _ := st.DedupClaim(ctx, "ops", "k", time.Millisecond); !ok {
			t.Fatal("outside the window the key must be claimable again")
		}
		if ok, _, _ := st.DedupClaim(ctx, "ops", "once", 0); !ok {
			t.Fatal("first once-ever claim")
		}
		if ok, _, _ := st.DedupClaim(ctx, "ops", "once", 0); ok {
			t.Fatal("window 0 means once, ever")
		}
		if err := st.DedupRelease(ctx, "ops", "k"); err != nil {
			t.Fatal(err)
		}
		if ok, _, _ := st.DedupClaim(ctx, "ops", "k", time.Hour); !ok {
			t.Fatal("a released claim must be claimable again")
		}
	})
}

// The claim is ONE statement: twenty runs racing for the same key produce
// exactly one send.
func TestDedupClaimIsAtomicUnderConcurrency(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, _, err := st.DedupClaim(ctx, "ops", "race", time.Hour)
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
		ctx := context.Background()
		exp := time.Now().Add(time.Hour)
		c := PauseCode{Channel: "ops", Code: "ab12cd", RunID: "r", StepID: "s", Kind: "approval", ExpiresAt: exp}
		if ok, err := st.InsertPauseCode(ctx, c); err != nil || !ok {
			t.Fatalf("insert ok=%v err=%v", ok, err)
		}
		other := c
		other.RunID = "r2"
		if ok, _ := st.InsertPauseCode(ctx, other); ok {
			t.Fatal("a colliding code must not be re-pointed")
		}
		got, err := st.PauseByCode(ctx, "ops", "ab12cd")
		if err != nil || got == nil || got.RunID != "r" || !got.Live(time.Now()) {
			t.Fatalf("got %+v err %v", got, err)
		}
		if got, _ := st.PauseByCode(ctx, "mail", "ab12cd"); got != nil {
			t.Fatal("a code must not match on another channel")
		}
		if err := st.RetirePauseCodes(ctx, "r", "s"); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.PauseByCode(ctx, "ops", "ab12cd"); got == nil || got.Live(time.Now()) {
			t.Fatalf("a retired code must not be live: %+v", got)
		}
		if err := st.PruneChannels(ctx, time.Now().Add(time.Minute), time.Now().Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.PauseByCode(ctx, "ops", "ab12cd"); got != nil {
			t.Fatal("retention must drop a retired code")
		}
	})
}

// A stored message is pending until it is handled.
func TestPendingMessagesUntilHandled(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		_, _ = st.InsertChannelMessage(ctx, &ChannelMessage{Channel: "p", MsgID: "1", Text: "x"})
		pend, err := st.PendingChannelMessages(ctx, "p")
		if err != nil || len(pend) != 1 || pend[0].Handled {
			t.Fatalf("pending %v err %v", pend, err)
		}
		_ = st.SetChannelMessageOutcome(ctx, "p", "1", "", "")
		if pend, _ := st.PendingChannelMessages(ctx, "p"); len(pend) != 0 {
			t.Fatal("a handled message is not pending")
		}
	})
}
