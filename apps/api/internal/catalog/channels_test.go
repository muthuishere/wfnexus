package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// A channel is a COMMAND entry; a bad one is skipped and reported, never a
// boot failure, and the allow list matches exactly.
func TestChannelsLoadFromTheRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registries.json")
	raw := `{"channels": {
		"ops":  {"kind": "command", "run": "node adapters/apl-whatsapp.js", "inbound": true, "allow": ["1555@s.whatsapp.net"], "receiveEvery": "1m"},
		"nope": {"kind": "command"},
		"slow": {"kind": "command", "run": "x", "sendGap": "soon"},
		"off":  {"kind": "command", "run": "x", "enabled": false}
	}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Channels.Len() != 1 {
		t.Fatalf("want only ops loaded, got %v (skips %v)", c.Channels.Names(), c.Channels.Skips())
	}
	ops, _ := c.Channels.Get("ops")
	if !ops.Inbound || !ops.Allows("1555@s.whatsapp.net") || ops.Allows("") || ops.Allows("1555") {
		t.Fatalf("ops %+v", ops)
	}
	if len(c.Channels.Skips()) != 3 {
		t.Fatalf("skips %v", c.Channels.Skips())
	}
}
