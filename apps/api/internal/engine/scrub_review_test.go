package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"
)

const cutSecret = "Zq7Wx3Lm9Pt2"

// A cut that falls inside a secret leaves a prefix no whole form matches; the
// prefix at the cut is redacted too, for each marker the platform appends.
func TestASecretCutByTruncationLeavesNoPrefix(t *testing.T) {
	secrets := []secretValue{{name: "CUT_KEY", value: cutSecret}}
	for _, text := range []string{
		"output ... " + cutSecret[:7] + "\n… truncated",                               // the worker's 64 KB cap
		"output ... " + cutSecret[:7] + "\n… (1234 more bytes)",                       // the engine's trim
		"ok: found " + cutSecret[:7] + "…",                                            // an outcome line
		"ok: found " + base64.StdEncoding.EncodeToString([]byte(cutSecret))[:9] + "…", // a cut base64 form
	} {
		got := redactTextCut(text, secrets)
		if strings.Contains(got, cutSecret[:minCutPrefix]) && !strings.Contains(got, "[redacted:CUT_KEY]") {
			t.Errorf("prefix kept: %q", got)
		}
		if !strings.Contains(got, "[redacted:CUT_KEY]") {
			t.Errorf("no marker: %q", got)
		}
	}
	// No cut, no guessing: text that merely ends like a secret is left alone.
	if got := redactTextCut("ends with "+cutSecret[:6], secrets); got != "ends with "+cutSecret[:6] {
		t.Errorf("an uncut text was changed: %q", got)
	}
}

// Basic auth puts the secret at any byte offset of the encoded user:secret.
func TestBasicAuthIsCaughtAtEveryOffset(t *testing.T) {
	secrets := []secretValue{{name: "PASS", value: cutSecret}}
	for _, user := range []string{"u", "us", "use", "user", "users"} {
		hdr := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+cutSecret))
		got := redactText(hdr, secrets)
		if !strings.Contains(got, "[redacted:PASS]") {
			t.Errorf("user %q: not caught: %q", user, got)
		}
	}
}

// A paused step's question goes to webhooks and chat; it is scrubbed first.
func TestAPausedQuestionIsScrubbed(t *testing.T) {
	secrets := []secretValue{{name: "Q_KEY", value: cutSecret}}
	got := scrubPending(&tn.Request{ID: "r1", Kind: "input", Prompt: "is " + cutSecret + " right?"}, secrets)
	if strings.Contains(got.Prompt, cutSecret) || got.ID != "r1" || got.Kind != "input" {
		t.Fatalf("got %+v", got)
	}
}

// After a restart the engine has no redactions in memory. A worker's late
// report is still scrubbed of what the job itself carried: the step's
// credential-named env and the module's door token.
func TestALateWorkerReportIsScrubbedFromTheJobsOwnEnv(t *testing.T) {
	e := &Engine{} // a fresh engine: nothing registered
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{
		"API_TOKEN":        cutSecret,
		"WFX_MODULE_TOKEN": "wfxm_" + cutSecret,
		"REGION":           "eu-central-1x",
	}})
	res := e.ScrubJobResult(context.Background(), uuid.New(), payload, JobResult{
		OK: true, Stdout: "token " + cutSecret + " door wfxm_" + cutSecret + " region eu-central-1x",
	})
	if strings.Contains(res.Stdout, cutSecret) || !strings.Contains(res.Stdout, "eu-central-1x") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}
