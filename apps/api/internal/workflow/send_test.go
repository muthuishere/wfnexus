package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `send:` step and a `message:` trigger load from YAML, and a send step is
// its own kind — not mixable with an agent.
func TestSendStepAndMessageTriggerLoad(t *testing.T) {
	dir := t.TempDir()
	good := `name: watch
on:
  message:
    - channel: ops
      match: "(?i)^deploy"
steps:
  - id: tell
    send:
      text: "hi {{ .Input.message.text }}"
      dedup: {key: "k", window: 15m}
`
	if err := os.WriteFile(filepath.Join(dir, "watch.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	defs, err := LoadDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := defs["watch"]
	if d == nil || d.Steps[0].Send == nil || d.Steps[0].Send.Dedup.Window != "15m" {
		t.Fatalf("send step not loaded: %+v", d)
	}
	if !d.On.Allows(TriggerMessage) || d.On.Allows(TriggerDispatch) {
		t.Fatalf("triggers %v", d.On.Names())
	}
	if !d.On.MatchesMessage("ops", "Deploy now") || d.On.MatchesMessage("ops", "hello") || d.On.MatchesMessage("mail", "deploy") {
		t.Fatal("message trigger matching is wrong")
	}

	bad := strings.Replace(good, "    send:", "    prompt: x\n    send:", 1)
	bad = strings.Replace(bad, "name: watch", "name: mixed", 1)
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "mixed.yaml"), []byte(bad), 0o600)
	if _, err := LoadDir(dir2, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("prompt + send must be refused: %v", err)
	}
}
