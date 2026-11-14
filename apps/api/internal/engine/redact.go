package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

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
//     text — in the raw, base64, hex and URL-encoded forms, with every JSON
//     string decoded first (redactEvent). An agent that runs `env` is thereby
//     caught too.
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
	if len(secrets) == 0 || text == "" {
		return text
	}
	return redactorFor(secrets).text(text)
}

// redactEvent returns payload with every sensitive value of the run replaced.
// It reaches every string at every depth, whatever the payload's Go type, and
// scrubs each one as the TEXT it is: a JSON string is decoded before it is
// scrubbed, so however a tool escaped it (\/, \uXXXX, & as &) the secret
// is found in its real spelling. A string whose text is itself JSON (a step
// that printed a JSON document) is decoded and scrubbed the same way, to
// maxJSONDepth levels.
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
	out, changed := redactorFor(secrets).tree(v, 0)
	if !changed {
		return payload
	}
	return out
}

// maxJSONDepth is how many levels of JSON-inside-a-string are decoded and
// scrubbed. Deeper than that, a string is scrubbed as text only.
const maxJSONDepth = 4

// truncation markers the platform appends when it shortens a text (the
// engine's trim, the worker's 64 KB cap, an outcome line, the step summary
// file). A cut can fall in the middle of a secret, leaving a prefix no full
// form matches.
var cutMarkers = []string{"\n… truncated", "\n… (", summaryCutMarker, "…"}

// minCutPrefix is the shortest secret prefix redacted at a cut; shorter tails
// are left alone, so an ordinary truncated output is not mislabelled.
const minCutPrefix = 4

// A redactor is everything one set of secrets needs, computed once: every
// form of every secret, and one replacer that finds them all in a single pass
// over a text. Redactors are cached by the secrets they were built for, so a
// run's events share one.
type redactor struct {
	forms []secretForm
	rep   *strings.Replacer
	// b64 are the base64 forms, for base64 wrapped across lines (a PEM body).
	b64 []secretForm
}

type secretForm struct{ form, name string }

var (
	redactorMu    sync.Mutex
	redactorCache = map[string]*redactor{}
)

func redactorFor(secrets []secretValue) *redactor {
	var key strings.Builder
	for _, sv := range secrets {
		key.WriteString(sv.name)
		key.WriteByte(0)
		key.WriteString(sv.value)
		key.WriteByte(0)
	}
	k := key.String()
	redactorMu.Lock()
	defer redactorMu.Unlock()
	if r := redactorCache[k]; r != nil {
		return r
	}
	if len(redactorCache) >= 256 {
		redactorCache = map[string]*redactor{}
	}
	r := newRedactor(secrets)
	redactorCache[k] = r
	return r
}

func newRedactor(secrets []secretValue) *redactor {
	r := &redactor{}
	seen := map[string]bool{}
	for _, sv := range secrets {
		for _, f := range secretForms(sv.value) {
			if !seen[f] {
				seen[f] = true
				r.forms = append(r.forms, secretForm{f, sv.name})
			}
		}
		for _, f := range base64Forms([]byte(sv.value)) {
			if len(f) >= minRedactLen {
				r.b64 = append(r.b64, secretForm{f, sv.name})
			}
		}
	}
	// Longest first: at one position the replacer takes the first pair that
	// matches, so a form that contains another is replaced whole.
	sort.SliceStable(r.forms, func(i, j int) bool { return len(r.forms[i].form) > len(r.forms[j].form) })
	sort.SliceStable(r.b64, func(i, j int) bool { return len(r.b64[i].form) > len(r.b64[j].form) })
	pairs := make([]string, 0, 2*len(r.forms))
	for _, f := range r.forms {
		pairs = append(pairs, f.form, "[redacted:"+f.name+"]")
	}
	r.rep = strings.NewReplacer(pairs...)
	return r
}

// text scrubs a plain text: every form in one pass, base64 wrapped across
// lines, and a secret's prefix left at a cut.
func (r *redactor) text(text string) string {
	text = r.rep.Replace(text)
	text = r.wrapped(text)
	return r.cut(text)
}

// tree scrubs every string (keys included) of a decoded JSON value.
func (r *redactor) tree(v any, depth int) (any, bool) {
	switch t := v.(type) {
	case string:
		s := r.str(t, depth)
		return s, s != t
	case []any:
		changed := false
		for i, x := range t {
			y, c := r.tree(x, depth)
			t[i], changed = y, changed || c
		}
		return t, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(t))
		for k, x := range t {
			y, c := r.tree(x, depth)
			nk := r.str(k, depth)
			out[nk] = y
			changed = changed || c || nk != k
		}
		return out, changed
	}
	return v, false
}

// str scrubs one decoded string. When its text is itself a JSON document, that
// is decoded and scrubbed as a tree (its strings decoded in turn) and written
// back; otherwise it is scrubbed as text.
func (r *redactor) str(s string, depth int) string {
	if depth < maxJSONDepth {
		if t := strings.TrimSpace(s); len(t) > 1 && strings.ContainsAny(t[:1], "{[\"") {
			var inner any
			if json.Unmarshal([]byte(t), &inner) == nil {
				out, changed := r.tree(inner, depth+1)
				if !changed {
					return r.text(s) // nothing inside: still scrub the text around it
				}
				// Written back as close to how it came as JSON allows: indented
				// if it was, and & < > left as they are.
				var buf bytes.Buffer
				enc := json.NewEncoder(&buf)
				enc.SetEscapeHTML(false)
				if strings.Contains(t, "\n") {
					enc.SetIndent("", "  ")
				}
				_ = enc.Encode(out)
				lead := s[:strings.Index(s, t)]
				return lead + strings.TrimSuffix(buf.String(), "\n") + s[len(lead)+len(t):]
			}
		}
	}
	return r.text(s)
}

// reWrapped finds runs of base64 broken into lines (a PEM body, `base64` at its
// default 76 columns).
var reWrapped = regexp.MustCompile(`(?:[A-Za-z0-9+/=_-]{16,}\r?\n)+[A-Za-z0-9+/=_-]+`)

// wrapped scrubs base64 forms that cross a line break: each wrapped run is
// read with its line breaks removed, and a hit is mapped back to the span of
// the original text, line breaks included.
func (r *redactor) wrapped(text string) string {
	if len(r.b64) == 0 || !strings.Contains(text, "\n") {
		return text
	}
	locs := reWrapped.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		run := text[loc[0]:loc[1]]
		// pos[i] is where the i-th non-line-break byte of run sits in run.
		flat := make([]byte, 0, len(run))
		pos := make([]int, 0, len(run))
		for i := 0; i < len(run); i++ {
			if run[i] != '\n' && run[i] != '\r' {
				flat = append(flat, run[i])
				pos = append(pos, i)
			}
		}
		f := string(flat)
		type span struct {
			start, end int
			name       string
		}
		var hits []span
		taken := make([]bool, len(run))
		for _, sf := range r.b64 { // longest forms first
			for at := 0; ; {
				i := strings.Index(f[at:], sf.form)
				if i < 0 {
					break
				}
				s, e := pos[at+i], pos[at+i+len(sf.form)-1]+1
				free := true
				for k := s; k < e; k++ {
					free = free && !taken[k]
				}
				if free {
					for k := s; k < e; k++ {
						taken[k] = true
					}
					hits = append(hits, span{s, e, sf.name})
				}
				at += i + len(sf.form)
			}
		}
		b.WriteString(text[last:loc[0]])
		if len(hits) == 0 {
			b.WriteString(run)
		} else {
			sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
			at := 0
			for _, h := range hits {
				b.WriteString(run[at:h.start])
				b.WriteString("[redacted:" + h.name + "]")
				at = h.end
			}
			b.WriteString(run[at:])
		}
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// cut redacts, when the text was shortened, a prefix of a secret form left
// right before the cut.
func (r *redactor) cut(text string) string {
	for _, m := range cutMarkers {
		i := strings.LastIndex(text, m)
		if i < 0 || (m == "…" && i+len(m) != len(text)) {
			continue
		}
		head := text[:i]
		for _, sf := range r.forms {
			for k := min(len(sf.form)-1, len(head)); k >= minCutPrefix; k-- {
				if strings.HasSuffix(head, sf.form[:k]) {
					head = head[:len(head)-k] + "[redacted:" + sf.name + "]"
					break
				}
			}
		}
		return head + text[i:]
	}
	return text
}

// redactTextCut is redactText; the cut handling is part of every text scrub.
func redactTextCut(text string, secrets []secretValue) string { return redactText(text, secrets) }

// secretForms is how a value can appear in a text:
//   - as itself, and with CRLF line endings when it spans lines (a PEM key);
//   - base64 (base64Forms);
//   - hex (either case) and URL-encoded (query and path forms);
//   - each of those JSON-escaped once, in Go's spelling and the common one,
//     for a JSON fragment inside text that is not itself a JSON document (a
//     log line). A JSON document is decoded instead (redactor.str), so any
//     escape spelling is found there.
func secretForms(v string) []string {
	b := []byte(v)
	raw := []string{v, hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)), url.QueryEscape(v), url.PathEscape(v)}
	if strings.Contains(v, "\n") && !strings.Contains(v, "\r\n") {
		raw = append(raw, strings.ReplaceAll(v, "\n", "\r\n"))
	}
	raw = append(raw, base64Forms(b)...)
	seen := map[string]bool{}
	var forms []string
	add := func(f string) {
		if len(f) >= minRedactLen && !seen[f] {
			seen[f] = true
			forms = append(forms, f)
		}
	}
	for _, f := range raw {
		add(f)
		add(jsonInner(f, true))
		add(jsonInner(f, false))
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// base64Forms is a value's base64 spellings: standard and URL alphabets,
// padded or not, and at each of the three byte offsets the value can sit at
// inside a longer encoded run (the form an Authorization header or `curl -u
// user:secret` leaves behind).
func base64Forms(b []byte) []string {
	out := []string{
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
	}
	out = append(out, base64Inner(b, base64.RawStdEncoding)...)
	return append(out, base64Inner(b, base64.RawURLEncoding)...)
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
	// One 0700 directory per job, never a folder shared across runs or
	// projects: under the run's own directory on the platform, under the
	// machine's temp dir on a worker (which has no work dir). It is named for
	// this process, so a worker that died mid-job has its leftovers swept at
	// the next start (SweepStaleSecrets).
	parent := secretsBase()
	if e.cfg.WorkDir != "" {
		parent = filepath.Join(e.cfg.WorkDir, runID.String(), "secrets")
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, nil, err
	}
	dir, err := os.MkdirTemp(parent, fmt.Sprintf("job-%d-", os.Getpid()))
	if err != nil {
		return "", nil, nil, err
	}
	drop := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "step.env")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		drop()
		return "", nil, nil, err
	}
	return fmt.Sprintf(". %s && ", workflow.ShellQuote(path)), rest, drop, nil
}

// secretsBase is where a machine with no work dir (a worker) keeps its
// per-job secret directories.
func secretsBase() string { return filepath.Join(os.TempDir(), "wfx-secrets") }

// SweepStaleSecrets removes the per-job secret directories of processes that
// are gone — left behind when a worker was killed mid-job, so its deferred
// cleanup never ran. A worker calls it when it starts. It returns how many it
// removed.
func SweepStaleSecrets() int {
	entries, err := os.ReadDir(secretsBase())
	if err != nil {
		return 0
	}
	n := 0
	for _, en := range entries {
		var pid int
		if _, err := fmt.Sscanf(en.Name(), "job-%d-", &pid); err != nil || processAlive(pid) {
			continue
		}
		if os.RemoveAll(filepath.Join(secretsBase(), en.Name())) == nil {
			n++
		}
	}
	return n
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // FindProcess opened it, so it exists
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
