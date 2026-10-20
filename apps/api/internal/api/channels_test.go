package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// [SEC-TEST] /api/channels/messages is project-scoped: a member sees the
// messages that answered or started runs in their project; a message that
// matched nothing is shown only to an unscoped admin.
func TestChannelMessagesAreScopedByProject(t *testing.T) {
	h, st := testServer(t, ":8090")
	ctx := context.Background()
	runA, _ := st.CreateRun(ctx, "acme", "w", json.RawMessage(`{}`))
	runB, _ := st.CreateRun(ctx, "other", "w", json.RawMessage(`{}`))
	for _, id := range []string{"answered-acme", "started-other", "unmatched"} {
		if _, err := st.InsertChannelMessage(ctx, &store.ChannelMessage{Channel: "team", MsgID: id, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetChannelMessageOutcome(ctx, "team", "answered-acme", runA.ID.String(), "resolved")
	_, _ = st.ClaimMessageRun(ctx, "team", "started-other", "w")
	_ = st.SetMessageRun(ctx, "team", "started-other", "w", runB.ID.String())

	list := func(tok string) []string {
		t.Helper()
		w := do(h, "GET", "/api/channels/messages", tok, nil)
		var rows []*store.ChannelMessage
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var ids []string
		for _, m := range rows {
			ids = append(ids, m.MsgID)
		}
		return ids
	}
	if got := list(user(t, st, "ana", "viewer", "acme")); len(got) != 1 || got[0] != "answered-acme" {
		t.Fatalf("an acme member saw %v", got)
	}
	if got := list(user(t, st, "bob", "publisher", "other")); len(got) != 1 || got[0] != "started-other" {
		t.Fatalf("an other member saw %v", got)
	}
	if got := list(user(t, st, "vic", "viewer", "")); len(got) != 2 {
		t.Fatalf("an unscoped NON-admin must see linked messages only, saw %v", got)
	}
	if got := list(user(t, st, "root", "admin", "")); len(got) != 3 {
		t.Fatalf("an unscoped admin sees everything, saw %v", got)
	}
	if got := list(user(t, st, "scopedadmin", "admin", "acme")); len(got) != 1 {
		t.Fatalf("a project scope wins even for an admin, saw %v", got)
	}
}
