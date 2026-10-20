package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The JSON Schemas in docs/schemas ARE the contract an adapter author codes
// against, so what the server actually emits must validate against them —
// otherwise the doc drifts from the code and nobody notices.
func schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "docs", "schemas", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	id := doc.(map[string]any)["$id"].(string)
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, v any) error {
	t.Helper()
	raw, _ := json.Marshal(v)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

func TestWhatTheServerEmitsMatchesTheSchemas(t *testing.T) {
	env := Envelope{Version: Version, Type: TypePause, Channel: "c", Text: "t", Code: "a7k2",
		RunURL: "http://wfx.internal/runs/1", At: time.Now().UTC(),
		Attachment: &Attachment{Path: "/tmp/x", Filename: "x"}}
	if err := validate(t, schema(t, "channel-send-stdin.schema.json"), env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	env.Attachment = &Attachment{}
	if err := validate(t, schema(t, "channel-send-stdin.schema.json"), env); err == nil {
		t.Fatal("an attachment with neither path nor url must not validate")
	}
	req := ReceiveRequest{Version: Version, Channel: "c"}
	if err := validate(t, schema(t, "channel-receive-stdin.schema.json"), req); err != nil {
		t.Fatalf("receive request: %v", err)
	}
	at := time.Now().UTC()
	res := ReceiveResult{Messages: []Message{{ID: "1", From: "f", Text: "x", At: &at}}, Cursor: "1"}
	if err := validate(t, schema(t, "channel-receive-stdout.schema.json"), res); err != nil {
		t.Fatalf("receive result: %v", err)
	}
	if err := validate(t, schema(t, "channel-send-stdout.schema.json"), SendResult{ID: "x"}); err != nil {
		t.Fatalf("send result: %v", err)
	}
}

// A failing adapter's stderr is the error, and the envelope reaches stdin.
func TestCommandSendsTheEnvelopeOnStdinAndReportsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	ok := Command{EntryName: "c", Run: "cat > " + filepath.Join(dir, "in.json") + "; echo '{\"id\":\"42\"}'; true"}
	// The verb is appended; `true send` is still true.
	res, err := ok.Send(context.Background(), Envelope{Text: "hello"})
	if err != nil || res.ID != "42" {
		t.Fatalf("res %+v err %v", res, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "in.json"))
	if !strings.Contains(string(raw), `"text":"hello"`) || !strings.Contains(string(raw), `"version":"wfnexus.channel/v1"`) {
		t.Fatalf("stdin %s", raw)
	}
	bad := Command{EntryName: "c", Run: "echo 'gateway down' >&2; exit 3; true"}
	if _, err := bad.Send(context.Background(), Envelope{Text: "x"}); err == nil || !strings.Contains(err.Error(), "gateway down") {
		t.Fatalf("err %v", err)
	}
	slow := Command{EntryName: "c", Run: "sleep 5;", Timeout: 200 * time.Millisecond}
	if _, err := slow.Send(context.Background(), Envelope{Text: "x"}); !errors.Is(err, ErrMaybeDelivered) {
		t.Fatalf("timeout err %v", err)
	}
}

// An attachment URL that would make the adapter fetch from inside the network
// is refused before it reaches the adapter.
func TestAttachmentURLRefusesInternalTargets(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://example.com/x", "file:///etc/passwd", "https://127.0.0.1/x", "https://10.0.0.5/x",
		"https://169.254.169.254/latest/meta-data", "https://[::1]/x", "https://192.168.1.2/x",
		"https://100.64.1.1/x", "https://localhost/x",
	} {
		if err := CheckAttachmentURL(ctx, u); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	if err := CheckAttachmentURL(ctx, "https://93.184.215.14/report.pdf"); err != nil {
		t.Fatalf("a public https address must pass: %v", err)
	}
}

// The adapter gets a minimal environment: never the server's secrets, only
// what the channel names.
func TestAdapterEnvIsMinimal(t *testing.T) {
	t.Setenv("WFX_DATABASE_URL", "postgres://secret")
	t.Setenv("OPENROUTER_API_KEY", "sk-secret")
	t.Setenv("TEAM_GROUP", "1203@g.us")
	env := strings.Join(AdapterEnv(map[string]string{"APL_WA_GROUP": "${TEAM_GROUP}"}), "\n")
	if strings.Contains(env, "secret") {
		t.Fatalf("server secrets leaked into the adapter env:\n%s", env)
	}
	if !strings.Contains(env, "APL_WA_GROUP=1203@g.us") || !strings.Contains(env, "PATH=") {
		t.Fatalf("env %s", env)
	}
}
