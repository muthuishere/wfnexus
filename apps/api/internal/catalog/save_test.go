package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpRegistry(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "registries.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A UI edit must not quietly delete the parts of the file it does not
// understand. registries.json opens with a `_comment` array explaining itself.
func TestSavePreservesUnknownKeys(t *testing.T) {
	p := tmpRegistry(t, `{
	  "_comment": ["this explains the file and must survive an edit"],
	  "providers": {"sonnet": {"kind":"http","baseUrl":"https://x/v1","style":"openai","model":"m","apiKeyEnv":"K"}}
	}`)
	if err := SaveProvider(p, Provider{
		Name: "haiku", Kind: KindHTTP, BaseURL: "https://x/v1", Style: "openai", Model: "h", APIKeyEnv: "K",
	}); err != nil {
		t.Fatal(err)
	}
	var f map[string]json.RawMessage
	raw, _ := os.ReadFile(p)
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if _, ok := f["_comment"]; !ok {
		t.Fatal("_comment was dropped")
	}

	// Both the existing and the new entry load.
	c, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sonnet", "haiku"} {
		if !c.Providers.Has(name) {
			t.Errorf("%s did not survive the write", name)
		}
	}
}

// The API must not be able to create an entry the loader would skip, or the UI
// accepts things that silently never load.
func TestSaveRefusesWhatTheLoaderWouldSkip(t *testing.T) {
	p := tmpRegistry(t, `{}`)
	// http with no baseUrl/model is exactly what validateProvider rejects.
	if err := SaveProvider(p, Provider{Name: "broken", Kind: KindHTTP, APIKeyEnv: "K"}); err == nil {
		t.Fatal("an invalid provider was written")
	}
	if err := SaveProvider(p, Provider{Name: "", Kind: KindHTTP}); err == nil {
		t.Fatal("a nameless provider was written")
	}
	if err := SaveProvider(p, Provider{Name: "nokind", Kind: "telepathy"}); err == nil {
		t.Fatal("an unknown kind was written")
	}
}

// apiKeyEnv holds the NAME of a variable. Pasting a key there would write a
// live credential into a file destined for version control, so the slip is
// refused rather than stored.
func TestSaveRefusesAKeyInTheKeyEnvField(t *testing.T) {
	p := tmpRegistry(t, `{}`)
	// None of these imitates a real key's prefix. An obviously-fake string in
	// a vendor's key FORMAT is still matched by secret scanners — GitHub push
	// protection rejected this file's first version over a fixture that was
	// never a credential — so the length branch is exercised with a run of
	// filler instead.
	for _, bad := range []string{
		"YOUR_KEY_HERE_" + strings.Repeat("X", 60), // too long to be a var name
		"my key",       // whitespace
		"secret!value", // punctuation
		"Bearer abc",   // a header value, not a name
		"KEY=value",    // an assignment
	} {
		err := SaveProvider(p, Provider{
			Name: "p", Kind: KindHTTP, BaseURL: "https://x/v1", Style: "openai", Model: "m", APIKeyEnv: bad,
		})
		if err == nil {
			t.Errorf("accepted %q as an env var name", bad)
			continue
		}
		if !strings.Contains(err.Error(), "NAME") {
			t.Errorf("unhelpful error for %q: %v", bad, err)
		}
	}
	// A real variable name is fine.
	if err := SaveProvider(p, Provider{
		Name: "p", Kind: KindHTTP, BaseURL: "https://x/v1", Style: "openai", Model: "m", APIKeyEnv: "OPENROUTER_API_KEY",
	}); err != nil {
		t.Fatalf("a legitimate env var name was refused: %v", err)
	}
}

// A local provider needs no key at all — the CLI holds its own credential.
func TestSaveALocalProvider(t *testing.T) {
	p := tmpRegistry(t, `{}`)
	if err := SaveProvider(p, Provider{Name: "claude-cli", Kind: KindCLI, Preset: "claude"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Providers.Get("claude-cli")
	if !ok || got.Preset != "claude" || got.Kind != KindCLI {
		t.Fatalf("round trip lost the entry: %+v ok=%v", got, ok)
	}
	// The name lives in the KEY, not duplicated in the value, so the two can
	// never disagree — but Load puts it back.
	if got.Name != "claude-cli" {
		t.Fatalf("Name not restored from the key: %q", got.Name)
	}
}

func TestSaveAndDeleteAClassifier(t *testing.T) {
	p := tmpRegistry(t, `{}`)
	if err := SaveClassifier(p, Classifier{Name: "judge", Backend: "static"}); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(p, "")
	if !c.Classifiers.Has("judge") {
		t.Fatal("classifier not written")
	}
	if err := DeleteClassifier(p, "judge"); err != nil {
		t.Fatal(err)
	}
	c, _ = Load(p, "")
	if c.Classifiers.Has("judge") {
		t.Fatal("classifier not deleted")
	}
}

// A write must touch only the entry being written. `Name` is carried in the map
// KEY on disk, and the rewrite re-marshals every entry — so without omitempty a
// single edit stamped `"name": ""` onto all of them. Caught by diffing
// registries.json after one API call.
func TestSaveLeavesOtherEntriesByteIdentical(t *testing.T) {
	const body = `{
	  "_comment": ["keep me"],
	  "providers": {
	    "sonnet": {"kind":"http","description":"the default worker","baseUrl":"https://o/v1","style":"openai","model":"m","apiKeyEnv":"K"},
	    "claude-cli": {"kind":"cli","description":"the local CLI","preset":"claude","repairs":2,"timeoutSec":900}
	  },
	  "classifiers": {"jev": {"description":"calibrated","backend":"openrouter","model":"typesafe/jev-1.13"}},
	  "mcpServers": {}
	}`
	p := tmpRegistry(t, body)

	var b0 map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &b0)

	if err := SaveClassifier(p, Classifier{Name: "new-judge", Backend: "static"}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(p)
	var after map[string]json.RawMessage
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}

	// Everything not being written is untouched, field for field.
	var wantP, gotP map[string]map[string]any
	_ = json.Unmarshal(b0["providers"], &wantP)
	_ = json.Unmarshal(after["providers"], &gotP)
	for name, want := range wantP {
		got := gotP[name]
		if len(got) != len(want) {
			t.Errorf("provider %s gained or lost fields: %v -> %v", name, want, got)
			continue
		}
		for k, v := range want {
			if fmt.Sprint(got[k]) != fmt.Sprint(v) {
				t.Errorf("provider %s field %s: %v -> %v", name, k, v, got[k])
			}
		}
	}
	// And no entry carries an empty name.
	for _, blob := range []json.RawMessage{after["providers"], after["classifiers"]} {
		if strings.Contains(string(blob), `"name":""`) {
			t.Errorf(`an entry was stamped with "name":"" — %s`, blob)
		}
	}
	if !strings.Contains(string(after["classifiers"]), "new-judge") {
		t.Error("the new classifier was not written")
	}
}
