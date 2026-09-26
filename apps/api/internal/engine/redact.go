package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// secretForms is how a value can appear inside a JSON-encoded event: as
// itself, JSON-escaped (a quote or backslash in it), and base64 — the form an
// Authorization header or a curl -u leaves behind.
func secretForms(v string) []string {
	forms := []string{v}
	if esc, err := json.Marshal(v); err == nil {
		if e := string(esc[1 : len(esc)-1]); e != v {
			forms = append(forms, e)
		}
	}
	forms = append(forms, base64.StdEncoding.EncodeToString([]byte(v)),
		strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(v)), "="))
	return forms
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
