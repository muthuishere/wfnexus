package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// The leak this guards against, exactly: a value from the sealed env store was
// written into the agent's bash command as NAME='value', and the tool call —
// command and all — was stored in the run's event log.
func TestASealedValueNeverReachesAnEvent(t *testing.T) {
	const secret = "NOT_A_REAL_KEY_but_long_enough_0123456789"
	var events []string
	e := &Engine{cfg: config.Config{WorkDir: t.TempDir()}}
	e.sink = func(kind string, payload any) {
		raw, _ := json.Marshal(payload)
		events = append(events, string(raw))
	}
	runID := uuid.New()
	env := map[string]string{"TYPESAFE_API_KEY": secret, "PLAIN_SETTING": "visible-value", "SHORT": "true"}
	e.redactions.add(runID, env, map[string]bool{"TYPESAFE_API_KEY": true})

	prefix, rest, drop, err := e.secretEnvFile(runID, "triage", env, e.redactions.sealedNames(runID))
	if err != nil {
		t.Fatal(err)
	}
	defer drop()
	if strings.Contains(prefix, secret) {
		t.Fatalf("the prefix carries the value: %q", prefix)
	}
	if _, ok := rest["TYPESAFE_API_KEY"]; ok {
		t.Fatal("the sealed variable was left for ShellPrefix to render")
	}
	// The file holds it, readable only by this user.
	path := strings.TrimSuffix(strings.TrimPrefix(prefix, ". '"), "' && ")
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", err, st)
	}

	h := e.hooks(context.Background(), runID, "triage", t.TempDir(), 10, rest, prefix)
	_, _ = h.BeforeTool(context.Background(), tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "wfx judge -q q.yaml"}})
	// An agent that prints its environment is caught by the second layer.
	_, _ = h.AfterTool(context.Background(), tn.AfterToolEvent{Name: "bash", Result: tn.ToolResult{
		Output: "TYPESAFE_API_KEY=" + secret + "\nauth: " + base64.StdEncoding.EncodeToString([]byte(secret))}})

	all := strings.Join(events, "\n")
	if len(events) < 2 {
		t.Fatalf("expected the tool call and its result, got %v", events)
	}
	for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
		if strings.Contains(all, form) {
			t.Fatalf("a secret value reached an event:\n%s", all)
		}
	}
	if !strings.Contains(all, "[redacted:TYPESAFE_API_KEY]") {
		t.Fatalf("the redaction is not visible, so nobody can tell a value was there:\n%s", all)
	}
	// Plain settings and short values survive — a log with every value blanked
	// is not a log.
	if !strings.Contains(all, "wfx judge -q q.yaml") {
		t.Fatalf("the command itself was lost:\n%s", all)
	}
}

// A credential-shaped NAME is sensitive even when it did not come from the
// sealed store (a literal in a step's env on a machine that allows it).
func TestACredentialNameIsRedactedFromAnyLayer(t *testing.T) {
	var r runSecrets
	id := uuid.New()
	r.add(id, map[string]string{"GITHUB_TOKEN": "ghp_NOT_A_REAL_TOKEN_123", "REGION": "eu-central-1x"}, nil)
	out, _ := json.Marshal(redactEvent(map[string]any{"text": "token ghp_NOT_A_REAL_TOKEN_123 region eu-central-1x"}, r.get(id)))
	if strings.Contains(string(out), "ghp_NOT_A_REAL_TOKEN_123") || !strings.Contains(string(out), "eu-central-1x") {
		t.Fatalf("got %s", out)
	}
}

// A project workflow acts on its project's repository unless told otherwise.
func TestAProjectRunDefaultsToItsRepository(t *testing.T) {
	got := withRepoDefault(map[string]any{"window_min": 20}, "/repos/reqsume")
	if got["repo_path"] != "/repos/reqsume" {
		t.Fatalf("no default: %v", got)
	}
	if got := withRepoDefault(map[string]any{"repo_path": "/elsewhere"}, "/repos/reqsume"); got["repo_path"] != "/elsewhere" {
		t.Fatalf("an explicit repo_path lost: %v", got)
	}
	if got := withRepoDefault(map[string]any{"repo_url": "https://x"}, "/repos/reqsume"); got["repo_path"] != nil {
		t.Fatalf("a repo_url run was pointed at the local checkout: %v", got)
	}
	if got := withRepoDefault(map[string]any{}, ""); got["repo_path"] != nil {
		t.Fatalf("a platform workflow got a repo: %v", got)
	}
}

// The hook, not just pinPaths: bash takes its own branch in BeforeTool, and a
// test of pinPaths alone passed while bash's relative workdir still went out
// unpinned.
func TestBashRelativeWorkdirIsPinnedByTheHook(t *testing.T) {
	e := &Engine{cfg: config.Config{WorkDir: t.TempDir()}}
	e.sink = func(string, any) {}
	ws := t.TempDir()
	h := e.hooks(context.Background(), uuid.New(), "review", ws, 10, nil, "")
	ov, err := h.BeforeTool(context.Background(), tn.BeforeToolEvent{Name: "bash",
		Args: map[string]any{"command": "git diff", "workdir": "fixes/abc"}})
	if err != nil || ov == nil {
		t.Fatalf("no override: %v", err)
	}
	if got := ov.Args["workdir"]; got != filepath.Join(ws, "fixes/abc") {
		t.Fatalf("bash workdir = %v, want it inside the workspace", got)
	}
}

// Found by a code-review run on a free model: a step's error quotes the
// agent's rejected reply, and error text did not pass through the run's
// redaction — only emit did. A model that echoed a secret leaked it there.
func TestAStepErrorQuotingAModelsReplyIsRedacted(t *testing.T) {
	secrets := []secretValue{{name: "GH_TOKEN", value: "ghp_averyrealtoken1234"}}
	err := `devinadapter: no valid reply; last reply: "{\"content\":\"the token is ghp_averyrealtoken1234\"}"`
	got := redactText(err, secrets)
	if strings.Contains(got, "ghp_averyrealtoken1234") {
		t.Fatalf("the secret survived in the error: %s", got)
	}
	if !strings.Contains(got, "[redacted:GH_TOKEN]") {
		t.Fatalf("no redaction mark: %s", got)
	}
}
