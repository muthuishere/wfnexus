package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const good = `{
  "providers": {
    "sonnet":   {"kind":"http","baseUrl":"https://openrouter.ai/api/v1","style":"openai","model":"anthropic/claude-sonnet-4.5","apiKeyEnv":"OPENROUTER_API_KEY"},
    "devin":    {"kind":"acp","preset":"devin"},
    "opencode": {"kind":"cli","preset":"opencode","repairs":2,"timeoutSec":900}
  },
  "classifiers": {
    "jev":    {"backend":"openrouter","model":"typesafe/jev-1.13"},
    "recorded": {"backend":"static"}
  },
  "mcpServers": {
    "github": {"type":"remote","url":"https://api.github.com/mcp"},
    "off":    {"type":"local","command":["x"],"enabled":false}
  }
}`

func TestLoadRegistersEveryKind(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "registries.json", good), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Providers.Names(); len(got) != 3 {
		t.Fatalf("providers = %v", got)
	}
	p, ok := c.Providers.Get("sonnet")
	if !ok || p.Kind != KindHTTP || p.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("sonnet = %+v", p)
	}
	if d, _ := c.Providers.Get("devin"); d.Kind != KindACP || d.Preset != "devin" {
		t.Fatalf("devin = %+v", d)
	}
	if o, _ := c.Providers.Get("opencode"); o.Kind != KindCLI || o.Repairs != 2 {
		t.Fatalf("opencode = %+v", o)
	}
	if got := c.Classifiers.Names(); len(got) != 2 {
		t.Fatalf("classifiers = %v", got)
	}
	// a disabled server is not offered, and says why
	if c.Mcp.Has("off") {
		t.Fatal("a disabled mcp server was registered")
	}
	if !c.Mcp.Has("github") {
		t.Fatal("github missing")
	}
	var sawDisabled bool
	for _, s := range c.Mcp.Skips() {
		if s.Reason == "disabled" {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatal("the disabled server was dropped without a reason")
	}
}

func TestLoadSkipsInvalidEntriesWithAReason(t *testing.T) {
	dir := t.TempDir()
	bad := `{"providers":{
	  "nokind":  {"baseUrl":"x","model":"y"},
	  "nostyle": {"kind":"http","baseUrl":"x","model":"y","style":"telepathy"},
	  "nourl":   {"kind":"http","style":"openai","model":"y"},
	  "nocmd":   {"kind":"cli"},
	  "weird":   {"kind":"smoke-signals"}
	},"classifiers":{"nob":{},"badb":{"backend":"vibes"}}}`
	c, err := Load(write(t, dir, "registries.json", bad), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers.Len() != 0 {
		t.Fatalf("invalid providers registered: %v", c.Providers.Names())
	}
	if len(c.Providers.Skips()) != 5 {
		t.Fatalf("skips = %d, want 5", len(c.Providers.Skips()))
	}
	if c.Classifiers.Len() != 0 || len(c.Classifiers.Skips()) != 2 {
		t.Fatalf("classifiers = %v skips=%v", c.Classifiers.Names(), c.Classifiers.Skips())
	}
}

// An absent file contributes nothing rather than failing the boot: a workflow
// that names nothing needs neither file.
func TestLoadToleratesMissingFiles(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"), filepath.Join(t.TempDir(), "also-nope.json"))
	if err != nil {
		t.Fatalf("a missing file should not fail the boot: %v", err)
	}
	if c.Providers.Len() != 0 || c.Mcp.Len() != 0 {
		t.Fatal("something appeared from nowhere")
	}
}

func TestLoadMergesASeparateMcpFile(t *testing.T) {
	dir := t.TempDir()
	main := write(t, dir, "registries.json", `{"mcpServers":{"a":{"type":"local","command":["a"]}}}`)
	side := write(t, dir, "mcp.json", `{"mcpServers":{"b":{"type":"local","command":["b"]}}}`)
	c, err := Load(main, side)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Mcp.Has("a") || !c.Mcp.Has("b") {
		t.Fatalf("merge lost a server: %v", c.Mcp.Names())
	}
}

func TestBadJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(write(t, dir, "registries.json", "{not json"), ""); err == nil {
		t.Fatal("malformed json was accepted")
	}
}

func TestMissingHelpers(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(write(t, dir, "registries.json", good), "")
	if got := c.MissingProviders([]string{"sonnet", "ghost"}); len(got) != 1 || got[0] != "ghost" {
		t.Fatalf("MissingProviders = %v", got)
	}
	if got := c.MissingClassifiers([]string{"jev"}); len(got) != 0 {
		t.Fatalf("MissingClassifiers = %v", got)
	}
	if got := c.MissingMcp([]string{"github", "off"}); len(got) != 1 || got[0] != "off" {
		t.Fatalf("a disabled server must read as missing: %v", got)
	}
}

// The style list is the set toolnexus's client actually implements, so it is
// asserted rather than described: an unrecognised style is NOT rejected
// downstream — the client tests only for "anthropic" and frames everything
// else as OpenAI — so a typo or an aspirational style would run, wrongly, and
// quietly. "gemini" is the live example: toolnexus exports ToGemini for TOOL
// SCHEMAS but has no Gemini ClientStyle, so naming it here would produce
// OpenAI framing under a Gemini name.
func TestOnlyImplementedWireStylesAreAccepted(t *testing.T) {
	if got := HTTPStyles(); len(got) != 2 || got[0] != "anthropic" || got[1] != "openai" {
		t.Fatalf("HTTPStyles() = %v", got)
	}
	dir := t.TempDir()
	js := `{"providers":{
	  "ok-openai":    {"kind":"http","baseUrl":"https://a.invalid/v1","model":"m","style":"openai"},
	  "ok-anthropic": {"kind":"http","baseUrl":"https://a.invalid","model":"m","style":"anthropic"},
	  "gemini-wire":  {"kind":"http","baseUrl":"https://a.invalid","model":"m","style":"gemini"}
	}}`
	c, err := Load(write(t, dir, "registries.json", js), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers.Len() != 2 {
		t.Fatalf("registered %v", c.Providers.Names())
	}
	if _, err := c.Providers.Require("gemini-wire"); err == nil {
		t.Fatal("an unimplemented wire style was accepted; it would have been framed as OpenAI")
	}
}

// An acp command is ARGV for a process that receives prompts over the
// protocol. A placeholder in it means the entry was written as a one-shot CLI
// and given the wrong kind — caught here rather than as an agent that answers
// turn one's question forever.
func TestAnACPCommandIsArgvNotAPromptTemplate(t *testing.T) {
	dir := t.TempDir()
	js := `{"providers":{
	  "acp-ok":  {"kind":"acp","command":["myacp","acp"]},
	  "acp-bad": {"kind":"acp","command":["myacp","-p","{{prompt}}"]},
	  "cli-bad": {"kind":"cli","command":["mycli","run"]}
	}}`
	c, err := Load(write(t, dir, "registries.json", js), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers.Len() != 1 || c.Providers.Names()[0] != "acp-ok" {
		t.Fatalf("registered %v", c.Providers.Names())
	}
	if len(c.Providers.Skips()) != 2 {
		t.Fatalf("skips = %v", c.Providers.Skips())
	}
}
