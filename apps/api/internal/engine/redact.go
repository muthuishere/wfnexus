package engine

import (
	"bytes"
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
	tn "github.com/muthuishere/toolnexus/golang"

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
// It reaches every string at every depth, whatever the payload's Go type, and
// scrubs each one as the TEXT it is: a step's stdout is a string inside the
// stored JSON, and a secret printed inside a JSON document a step wrote is
// found there in its once-escaped spelling, not escaped a second time by the
// way the platform happens to store it.
func redactEvent(payload any, secrets []secretValue) any {
	if len(secrets) == 0 || payload == nil {
		return payload
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return payload
	}
	out, changed := scrubTree(v, secrets)
	if !changed {
		return payload
	}
	return out
}

// scrubTree scrubs every string (keys included) of a decoded JSON value.
func scrubTree(v any, secrets []secretValue) (any, bool) {
	switch t := v.(type) {
	case string:
		s := redactTextCut(t, secrets)
		return s, s != t
	case []any:
		changed := false
		for i, x := range t {
			y, c := scrubTree(x, secrets)
			t[i], changed = y, changed || c
		}
		return t, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(t))
		for k, x := range t {
			y, c := scrubTree(x, secrets)
			nk := redactText(k, secrets)
			out[nk] = y
			changed = changed || c || nk != k
		}
		return out, changed
	}
	return v, false
}

// truncation markers the platform appends when it shortens a text (the
// engine's trim, the worker's 64 KB cap, an outcome line). A cut can fall in
// the middle of a secret, leaving a prefix no full form matches.
var cutMarkers = []string{"\n… truncated", "\n… (", "…"}

// minCutPrefix is the shortest secret prefix redacted at a cut; shorter tails
// are left alone, so an ordinary truncated output is not mislabelled.
const minCutPrefix = 4

// redactTextCut is redactText, plus: when the text was shortened, a prefix of a
// secret form left right before the cut is redacted too.
func redactTextCut(text string, secrets []secretValue) string {
	text = redactText(text, secrets)
	for _, m := range cutMarkers {
		i := strings.LastIndex(text, m)
		if i < 0 || (m == "…" && i+len(m) != len(text)) {
			continue
		}
		head := text[:i]
		for _, sv := range secrets {
			for _, form := range secretForms(sv.value) {
				for k := len(form) - 1; k >= minCutPrefix; k-- {
					if strings.HasSuffix(head, form[:k]) {
						head = head[:len(head)-k] + "[redacted:" + sv.name + "]"
						break
					}
				}
			}
		}
		return head + text[i:]
	}
	return text
}

// secretForms is how a value can appear in what a step prints or an event
// carries:
//   - as itself, and with CRLF line endings when it spans lines (a PEM key);
//   - base64, the form an Authorization header or a `curl -u user:secret`
//     leaves behind: standard and URL alphabets, padded or not, and at each of
//     the three byte offsets the value can sit at inside a longer encoded run;
//   - hex (either case) and URL-encoded (query and path forms);
//   - each of those JSON-escaped once (a JSON document a step printed) and
//     twice (such a document quoted inside another one).
func secretForms(v string) []string {
	b := []byte(v)
	raw := []string{v,
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
		hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)),
		url.QueryEscape(v), url.PathEscape(v),
	}
	if strings.Contains(v, "\n") && !strings.Contains(v, "\r\n") {
		raw = append(raw, strings.ReplaceAll(v, "\n", "\r\n"))
	}
	raw = append(raw, base64Inner(b, base64.RawStdEncoding)...)
	raw = append(raw, base64Inner(b, base64.RawURLEncoding)...)
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
		// Go escapes & < > as \u00XX; most other tools (python, jq, kubectl)
		// leave them as they are. Both spellings, once and twice escaped.
		for _, html := range []bool{true, false} {
			once := jsonInner(f, html)
			add(once)
			add(jsonInner(once, html))
		}
	}
	// Longest first, so a form that contains another is replaced whole.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// jsonInner is s as it reads inside a JSON string, without the quotes; html
// says whether & < > are escaped the way Go does it.
func jsonInner(s string, html bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(html)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	esc := strings.TrimRight(buf.String(), "\n")
	return esc[1 : len(esc)-1]
}

// base64Inner is the part of the base64 encoding of b that is the same
// whatever bytes come before and after it, for b starting 1 or 2 bytes into a
// 3-byte group (offset 0 is the plain encoding). That is how `user:secret`
// encodes the secret when the user name is not a multiple of 3 long.
func base64Inner(b []byte, enc *base64.Encoding) []string {
	var out []string
	for p := 1; p <= 2; p++ {
		full := enc.EncodeToString(append(make([]byte, p), b...))
		drop := p + 1 // chars that also depend on the p prefix bytes
		end := len(full)
		if (p+len(b))%3 != 0 {
			end-- // the last char also depends on the byte that follows
		}
		if end-drop >= minRedactLen {
			out = append(out, full[drop:end])
		}
	}
	return out
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

// scrubPending is a paused step's question with the run's secret values
// removed from every field.
func scrubPending(req *tn.Request, secrets []secretValue) *tn.Request {
	if req == nil || len(secrets) == 0 {
		return req
	}
	var out tn.Request
	if json.Unmarshal(mustJSON(redactEvent(req, secrets)), &out) != nil {
		return &tn.Request{ID: req.ID, Kind: req.Kind, Prompt: "this question carried a secret value and could not be scrubbed safely"}
	}
	return &out
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
