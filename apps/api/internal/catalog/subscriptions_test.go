package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubscriptionSaveLoadDelete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "registries.json")
	if err := os.WriteFile(p, []byte(`{"_comment":["keep me"],"channels":{"wa":{"kind":"command","run":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Subscription{Name: "pp", Channel: "wa", Chat: "g@g", Workflow: "pingpong", Interval: "30s", Status: SubActive}
	if err := SaveSubscription(p, s); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	got := c.Subscriptions["pp"]
	if got.Name != "pp" || got.Chat != "g@g" || got.Status != SubActive || got.EffectiveMode() != ModeExclusive {
		t.Fatalf("got %+v", got)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "keep me") || !strings.Contains(string(raw), `"channels"`) {
		t.Fatalf("other keys lost: %s", raw)
	}
	if err := DeleteSubscription(p, "pp"); err != nil {
		t.Fatal(err)
	}
	c, _ = Load(p, "")
	if len(c.Subscriptions) != 0 {
		t.Fatal("not deleted")
	}
}

func TestSubscriptionValidation(t *testing.T) {
	bad := []Subscription{
		{Name: "a", Channel: "wa", Chat: "c"}, // no workflow
		{Name: "a", Channel: "wa", Chat: "c", Workflow: "w", Interval: "5s"},
		{Name: "a", Channel: "wa", Chat: "c", Workflow: "w", Mode: "broadcast"},
		{Name: "a", Channel: "wa", Chat: "c", Workflow: "w", Events: []string{"typing"}},
		{Name: "a b", Channel: "wa", Chat: "c", Workflow: "w"},
	}
	for i, s := range bad {
		if ValidateSubscription(s) == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
	// A broken entry on disk loads with status error rather than vanishing.
	p := filepath.Join(t.TempDir(), "r.json")
	_ = os.WriteFile(p, []byte(`{"subscriptions":{"x":{"channel":"wa","chat":"c","workflow":"w","interval":"9h"}}}`), 0o644)
	c, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Subscriptions["x"].Status != SubError || c.Subscriptions["x"].Error == "" {
		t.Fatalf("%+v", c.Subscriptions["x"])
	}
	s := Subscription{Allow: nil}
	if s.AllowsSender("anyone") {
		t.Fatal("empty allow list must be nobody")
	}
}
