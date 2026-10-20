package store

import (
	"context"
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
		if ok, _, err := st.DedupClaim(ctx, "ops", "k", time.Hour); err != nil || ok {
			t.Fatalf("second claim inside the window ok=%v err=%v", ok, err)
		}
		if ok, _, _ := st.DedupClaim(ctx, "other", "k", time.Hour); !ok {
			t.Fatal("a key is per channel")
		}
		if err := st.DedupRelease(ctx, "ops", "k"); err != nil {
			t.Fatal(err)
		}
		if ok, _, _ := st.DedupClaim(ctx, "ops", "k", time.Hour); !ok {
			t.Fatal("a released claim must be claimable again")
		}
	})
}

func TestPauseCodesRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		if err := st.PutPauseCode(ctx, PauseCode{Code: "ab12", RunID: "r", StepID: "s", Kind: "approval"}); err != nil {
			t.Fatal(err)
		}
		c, err := st.PauseByCode(ctx, "ab12")
		if err != nil || c == nil || c.StepID != "s" || c.Kind != "approval" {
			t.Fatalf("got %+v err %v", c, err)
		}
		if c, _ := st.PauseByCode(ctx, "zzzz"); c != nil {
			t.Fatalf("unknown code found: %+v", c)
		}
	})
}
