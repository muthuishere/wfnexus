package engine

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// SECRET VALUES NEVER REACH THE EVENT LOG.
//
// The env store hands a step DECRYPTED values. The agent's bash tool has no env
// argument, so the engine used to put each variable in front of the command —
// `NAME='value' cmd` — and the tool call, command and all, went into the run's
// event log: a TypeSafe key sat in plain text on the run page and in every
// transcript that read it. ShellPrefix's promise ("only the name is logged")
// held for `${VAR}` references and for literals a file may commit, and silently
// broke for the one source that is secret by definition.
//
// Two layers, because the second must hold even when the first is bypassed:
//
//  1. A sensitive value is never written into a command. It goes into a 0600
//     file in the run's own directory, sourced by path; the path is what the
//     tool call records (secretEnvFile).
//  2. Every event is scrubbed of every sensitive value of its run before it is
//     stored — tool calls, tool output, a `run:` step's stdout, the model's own
//     text — in the raw, base64 and JSON-escaped forms (redactEvent). An agent
//     that runs `env` is thereby caught too.
//
// SENSITIVE means: every value from the platform's env store (a person put it
// in the sealed store; that is the statement), plus any value under a
// credential-shaped name from any layer. Values shorter than 6 characters are
// not redacted — replacing every "true" in a log would destroy it and protect
// nothing.

const minRedactLen = 6

type secretValue struct{ name, value string }

type runSecrets struct {
	mu     sync.Mutex
	byID   map[uuid.UUID][]secretValue
	sealed map[uuid.UUID]map[string]bool
}

// sealedNames are the variable names this run got from the platform's sealed
// store — secret by the act of storing them there.
func (r *runSecrets) sealedNames(runID uuid.UUID) map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sealed[runID]
}

func (r *runSecrets) add(runID uuid.UUID, env map[string]string, sealed map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[uuid.UUID][]secretValue{}
		r.sealed = map[uuid.UUID]map[string]bool{}
	}
	if r.sealed[runID] == nil {
		r.sealed[runID] = map[string]bool{}
	}
	for k := range sealed {
		r.sealed[runID][k] = true
	}
	have := map[string]bool{}
	for _, s := range r.byID[runID] {
		have[s.value] = true
	}
	for k, v := range env {
		if len(v) < minRedactLen || have[v] || !(sealed[k] || workflow.SecretName(k)) {
			continue
		}
		r.byID[runID] = append(r.byID[runID], secretValue{k, v})
		have[v] = true
	}
	// Longest first, so a value that contains another is replaced whole.
	sort.Slice(r.byID[runID], func(i, j int) bool { return len(r.byID[runID][i].value) > len(r.byID[runID][j].value) })
}

func (r *runSecrets) get(runID uuid.UUID) []secretValue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[runID]
}

func (r *runSecrets) forget(runID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, runID)
}

// redactText replaces every sensitive value of the run in a plain string —
// the step and run error text, which does not pass through emit. An agent's
// rejected reply is quoted in its error, and a model can echo a secret it saw.
func redactText(text string, secrets []secretValue) string {
	for _, sv := range secrets {
		for _, form := range secretForms(sv.value) {
			if form != "" {
				text = strings.ReplaceAll(text, form, "[redacted:"+sv.name+"]")
			}
		}
	}
	return text
}

// redactEvent returns payload with every sensitive value of the run replaced.
// It goes through JSON so it reaches every string at every depth, whatever the
// payload's Go type.
func redactEvent(payload any, secrets []secretValue) any {
	if len(secrets) == 0 || payload == nil {
		return payload
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	s := string(raw)
	changed := false
	for _, sv := range secrets {
		mark := "[redacted:" + sv.name + "]"
		for _, form := range secretForms(sv.value) {
			if strings.Contains(s, form) {
				s = strings.ReplaceAll(s, form, mark)
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		// A replacement broke the JSON (a value spanning an escape). Refuse to
		// store what could still carry the secret.
		return map[string]any{"redacted": "this event carried a secret value and could not be scrubbed safely"}
	}
	return out
}

// secretForms is how a value can appear in what a step prints or an event
// carries: as itself, JSON-escaped (a quote or backslash in it), base64 (the
// form an Authorization header or a curl -u leaves behind; standard and URL
// alphabets, padded or not), hex (either case) and URL-encoded (query and path
// forms). Each form also in its JSON-escaped spelling, since events and step
// outputs are stored as JSON.
func secretForms(v string) []string {
	b := []byte(v)
	raw := []string{v,
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
		hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)),
		url.QueryEscape(v), url.PathEscape(v),
	}
	seen := map[string]bool{}
	var forms []string
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			forms = append(forms, f)
		}
	}
	for _, f := range raw {
		add(f)
		if esc, err := json.Marshal(f); err == nil {
			add(string(esc[1 : len(esc)-1]))
		}
	}
	// Longest first, so a form that contains another is replaced whole.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// scrubJSON scrubs a stored JSON document (a step's output) the same way an
// event is scrubbed. Text that is not JSON is scrubbed as text.
func scrubJSON(raw []byte, secrets []secretValue) []byte {
	if len(raw) == 0 || len(secrets) == 0 {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return []byte(redactText(string(raw), secrets))
	}
	out, _ := json.Marshal(redactEvent(v, secrets))
	return out
}

// scrubOutput is scrubJSON for a step output as the engine holds it, so the
// steps after it read the scrubbed value too: secrets travel through the
// environment, never through step output.
func scrubOutput(out map[string]any, secrets []secretValue) map[string]any {
	if out == nil || len(secrets) == 0 {
		return out
	}
	var m map[string]any
	if json.Unmarshal(scrubJSON(mustJSON(out), secrets), &m) != nil {
		return map[string]any{"redacted": "this output carried a secret value and could not be scrubbed safely"}
	}
	return m
}

// secretEnvFile writes a step's sensitive variables to a 0600 file under the
// run's own directory and returns the command prefix that loads it, plus the
// env WITHOUT those variables (for ShellPrefix). The file path is what the tool
// call records; the values never enter a command string.
func (e *Engine) secretEnvFile(runID uuid.UUID, stepID string, env map[string]string, sealed map[string]bool) (prefix string, rest map[string]string, cleanup func(), err error) {
	rest = map[string]string{}
	var lines []string
	for k, v := range env {
		if (sealed[k] || workflow.SecretName(k)) && !workflow.IsEnvRef(v) {
			lines = append(lines, "export "+k+"="+workflow.ShellQuote(v))
			continue
		}
		rest[k] = v
	}
	if len(lines) == 0 {
		return "", env, func() {}, nil
	}
	sort.Strings(lines)
	dir := filepath.Join(e.cfg.WorkDir, runID.String(), "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, nil, err
	}
	path := filepath.Join(dir, stepID+".env")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", nil, nil, err
	}
	return fmt.Sprintf(". %s && ", workflow.ShellQuote(path)), rest, func() { _ = os.Remove(path) }, nil
}
