package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
	"gopkg.in/yaml.v3"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// `wfx judge` — typed questions on a calibrated classifier, for as many items
// as you have, from anywhere a shell is.
//
//	wfx judge -q questions.yaml --items candidates.jsonl > verdicts.jsonl
//	wfx judge -q questions.yaml --state "func actorOf ... no callers found"
//
// A workflow's `decide:` asks its questions ONCE, about one state, before a
// step starts. That is the wrong shape for "is each of these 40 candidates
// really dead": the answer is per item. So this is the same judge, callable per
// item — by an agent step through its shell, by a `run:` step, by a person, or
// by a Claude session rehearsing a workflow. The questions file has exactly the
// shape of `decide.questions`, so a question tuned here moves into a workflow
// unchanged, and back.
//
// Every noul comes back with a BAND — no (<0.30) / uncertain / yes (>0.70) by
// default, TypeSafe's own self-consistency cut — so "send the uncertain ones to
// a human" is a filter on a field, not arithmetic every caller redoes.
//
// The key is named, never passed: TYPESAFE_API_KEY for TypeSafe's API,
// OPENROUTER_API_KEY for the OpenRouter gateway. The library reads it at call
// time; this command never sees the value and never prints it.

type judgeItem struct {
	ID    string `json:"id"`
	State any    `json:"state"`
}

type judgeResult struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model,omitempty"`
	Calibrated bool                    `json:"calibrated"`
	Answers    map[string]judge.Answer `json:"answers,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

const judgeUsage = `wfx judge -q <questions.yaml> (--items <file.jsonl|-> | --state <text>)
          [--classifier <name> | --backend typesafe|openrouter | --url <base>]
          [--model m] [--key-env VAR] [--header K=V]... [--param k=v]...
          [--timeout sec] [--retries n] [--parallel 8] [--bands 0.30,0.70] [--local]

  questions.yaml   the shape of a workflow's decide.questions:
                     dead:
                       type: noul                   # noul | choice | score
                       instructions: Is this code unreachable at runtime?
                       true: nothing calls it, directly or indirectly
                       false: something calls it, even via reflection or a template
  --items          JSON Lines, one {"id": "...", "state": <anything>} per line; - for stdin
  --state          one state, inline (id "state")
  --classifier     a classifier registry entry — any JEV model configured on this
                   install (wfx registry classifiers); the same name a workflow
                   step gives in classifier:
  --backend        without --classifier: typesafe (TYPESAFE_API_KEY) or openrouter
                   (OPENROUTER_API_KEY); default typesafe when its key is set
  --url            any endpoint speaking the JEV (systemone) wire — self-hosted,
                   a proxy, a gateway; alone, or overriding an entry's base
  --model          any JEV model id, overriding the entry's or the backend's default
  --key-env        the NAME of the variable holding the key (never the key)
  --header K=V     extra header; credential headers must reference ${VAR}
  --param k=v      extra request-body field (JSON values typed)
  --timeout/--retries  per request, as toolnexus ClassifierOptions
  Every option maps one-to-one onto toolnexus ClassifierOptions.

  With WFX_API set (a workflow step always has it) and none of --backend/--url/
  --model/--key-env/--header/--param/--timeout/--retries, the items are judged
  ON THAT SERVER with its registry and its key: --classifier names the server's
  entry, and no --classifier means the server's default. --local judges here.

Writes one JSON line per item to stdout, in input order, each noul with its band
(no | uncertain | yes). A summary goes to stderr.`

func cmdJudge(args []string) error {
	qPath := flagOf(args, "-q", flagOf(args, "--questions", ""))
	if qPath == "" || hasFlag(args, "--help") || hasFlag(args, "-h") {
		fmt.Println(judgeUsage)
		if qPath == "" {
			return fmt.Errorf("-q <questions.yaml> is required")
		}
		return nil
	}
	questions, err := readQuestions(qPath)
	if err != nil {
		return err
	}
	tq, err := judge.Questions(questions)
	if err != nil {
		return err
	}
	bands, err := parseBands(flagOf(args, "--bands", ""))
	if err != nil {
		return err
	}
	items, err := readItems(flagOf(args, "--items", ""), flagOf(args, "--state", ""))
	if err != nil {
		return err
	}
	parallel, _ := strconv.Atoi(flagOf(args, "--parallel", "8"))
	if parallel < 1 {
		parallel = 1
	}

	start := time.Now()
	var results []judgeResult
	var cost float64
	var costKnown bool
	var backend string
	if judgeOnServer(args) {
		backend, results, cost, costKnown, err = judgeRemote(args, questions, items, bands, parallel)
		if err != nil {
			return err
		}
		return reportJudged(results, backend, start, cost, costKnown)
	}

	c, backend, err := judgeClassifier(args)
	if err != nil {
		return err
	}

	results = make([]judgeResult, len(items))
	var mu sync.Mutex
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it judgeItem) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			r := judgeResult{ID: it.ID}
			d, err := c.Evaluate(ctx, it.State, tq)
			if err == nil {
				r.Model, r.Calibrated = d.Model, d.Calibrated
				r.Answers, err = judge.Read(d, questions, bands)
				mu.Lock()
				if d.Usage.Cost != nil {
					cost += *d.Usage.Cost
					costKnown = true
				}
				mu.Unlock()
			}
			if err != nil {
				r.Error = err.Error()
			}
			results[i] = r
		}(i, it)
	}
	wg.Wait()
	return reportJudged(results, backend, start, cost, costKnown)
}

// judgeOnServer says whether this invocation should ask the server's
// classifier rather than build one here. It does when WFX_API names a server —
// which is what a workflow step has — and nothing on the command line points
// at a different endpoint. A step does not inherit the server's registry or
// its classifier key (and must not), so asking the server is the only way a
// step's `wfx judge --classifier jev` means the jev the server has.
// --local forces the in-process path.
func judgeOnServer(args []string) bool {
	if os.Getenv("WFX_API") == "" || hasFlag(args, "--local") {
		return false
	}
	if os.Getenv("WFX_JUDGE_BASE_URL") != "" {
		return false
	}
	for _, f := range []string{"--backend", "--url", "--model", "--key-env", "--header", "--param", "--timeout", "--retries"} {
		if hasFlag(args, f) {
			return false
		}
	}
	return true
}

// judgeRemote posts the questions and items to the server in batches.
func judgeRemote(args []string, questions map[string]workflow.Question, items []judgeItem, bands judge.Bands, parallel int) (string, []judgeResult, float64, bool, error) {
	name := flagOf(args, "--classifier", "")
	label := "server " + base()
	if name != "" {
		label = name + " on " + base()
	}
	var all []judgeResult
	var cost float64
	var costKnown bool
	const batch = 500
	for i := 0; i < len(items); i += batch {
		end := min(i+batch, len(items))
		var out struct {
			Results []judgeResult `json:"results"`
			CostUSD *float64      `json:"costUsd"`
		}
		body := map[string]any{"classifier": name, "questions": questions, "items": items[i:end],
			"bands": bands, "parallel": parallel}
		if err := call("POST", "/api/judge", body, &out); err != nil {
			return "", nil, 0, false, fmt.Errorf("judging on %s: %w (use --local to judge in this process)", base(), err)
		}
		all = append(all, out.Results...)
		if out.CostUSD != nil {
			cost += *out.CostUSD
			costKnown = true
		}
	}
	return label, all, cost, costKnown, nil
}

func reportJudged(results []judgeResult, backend string, start time.Time, cost float64, costKnown bool) error {
	items := results
	enc := json.NewEncoder(os.Stdout)
	failed, uncertain := 0, 0
	for _, r := range results {
		_ = enc.Encode(r)
		if r.Error != "" {
			failed++
		}
		for _, a := range r.Answers {
			if a.Band == "uncertain" {
				uncertain++
				break
			}
		}
	}
	costNote := "cost not reported by this backend"
	if costKnown {
		costNote = fmt.Sprintf("$%.4f", cost)
	}
	fmt.Fprintf(os.Stderr, "judged %d item(s) on %s in %s — %d uncertain (route to a human), %d failed, %s\n",
		len(items), backend, time.Since(start).Round(time.Millisecond), uncertain, failed, costNote)
	if failed > 0 {
		return fmt.Errorf("%d of %d item(s) could not be judged; see the error field", failed, len(items))
	}
	return nil
}

func readQuestions(path string) (map[string]workflow.Question, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs map[string]workflow.Question
	// YAML is a superset of JSON, so one decoder reads either.
	if err := yaml.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("%s: no questions", path)
	}
	return qs, nil
}

func readItems(path, inline string) ([]judgeItem, error) {
	if inline != "" {
		return []judgeItem{{ID: "state", State: inline}}, nil
	}
	if path == "" {
		return nil, fmt.Errorf("give --items <file.jsonl|-> or --state <text>")
	}
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	var out []judgeItem
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var it judgeItem
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("items line %d: %w", n, err)
		}
		if it.State == nil {
			return nil, fmt.Errorf("items line %d: no \"state\"", n)
		}
		if it.ID == "" {
			it.ID = strconv.Itoa(n)
		}
		out = append(out, it)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no items to judge")
	}
	return out, nil
}

func parseBands(s string) (judge.Bands, error) {
	if s == "" {
		return judge.DefaultBands, nil
	}
	lo, hi, ok := strings.Cut(s, ",")
	l, err1 := strconv.ParseFloat(strings.TrimSpace(lo), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(hi), 64)
	if !ok || err1 != nil || err2 != nil || l < 0 || h > 1 || l >= h {
		return judge.Bands{}, fmt.Errorf("--bands wants two cut points, low,high, with 0 <= low < high <= 1 (got %q)", s)
	}
	return judge.Bands{Low: l, High: h}, nil
}

// judgeClassifier builds the judge from, in order: a registry entry
// (`--classifier`), else a vendor preset (`--backend`), else a bare endpoint
// (`--url`) — and then the flags on top, each one a toolnexus
// ClassifierOptions field under its own name, so anything the library can be
// told, this command can be told:
//
//	--url, --model, --key-env, --header K=V (repeatable, ${VAR} expanded at
//	call time), --timeout <sec>, --retries <n>, --param k=v (repeatable,
//	merged into the request body)
//
// The base URL, model and key variable of a preset travel as a unit
// (judge.Options), and an explicit flag still wins over the preset — the
// library's own rule. WFX_JUDGE_BASE_URL is the environment spelling of --url.
func judgeClassifier(args []string) (*tn.Classifier, string, error) {
	var entry catalog.Classifier
	switch name := flagOf(args, "--classifier", ""); {
	case name != "":
		cfg := config.Load()
		cat, err := catalog.Load(cfg.RegistriesPath, cfg.McpConfig)
		if err != nil {
			return nil, "", err
		}
		if entry, err = cat.Classifiers.Require(name); err != nil {
			return nil, "", fmt.Errorf("%w (see `wfx registry classifiers`)", err)
		}
		entry.Name = name
	case flagOf(args, "--backend", "") != "":
		b := flagOf(args, "--backend", "")
		entry = catalog.Classifier{Name: b, Backend: b}
	case flagOf(args, "--url", os.Getenv("WFX_JUDGE_BASE_URL")) != "":
		entry = catalog.Classifier{Name: "url"}
	default:
		b := "openrouter"
		if os.Getenv("TYPESAFE_API_KEY") != "" {
			b = "typesafe"
		}
		entry = catalog.Classifier{Name: b, Backend: b}
	}

	if v := flagOf(args, "--url", os.Getenv("WFX_JUDGE_BASE_URL")); v != "" {
		entry.BaseURL = v
		if entry.Name == "url" {
			entry.Name = v
		}
	}
	if v := flagOf(args, "--model", ""); v != "" {
		entry.Model = v
	}
	if v := flagOf(args, "--key-env", ""); v != "" {
		entry.APIKeyEnv = v
	}
	if v := flagOf(args, "--timeout", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, "", fmt.Errorf("--timeout wants whole seconds > 0, got %q", v)
		}
		entry.TimeoutSec = n
	}
	if v := flagOf(args, "--retries", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, "", fmt.Errorf("--retries wants a whole number >= 0, got %q", v)
		}
		entry.Retries = n
	}
	for _, kv := range flagsOf(args, "--header") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, "", fmt.Errorf("--header wants K=V, got %q", kv)
		}
		if entry.Headers == nil {
			entry.Headers = map[string]string{}
		}
		entry.Headers[k] = v
	}
	for _, kv := range flagsOf(args, "--param") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, "", fmt.Errorf("--param wants k=v, got %q", kv)
		}
		if entry.RequestParams == nil {
			entry.RequestParams = map[string]any{}
		}
		var val any = v
		_ = json.Unmarshal([]byte(v), &val) // numbers, booleans and JSON go through typed
		entry.RequestParams[k] = val
	}
	// The same validation a saved registry entry passes — a literal key in a
	// credential header is refused here exactly as it is in registries.json.
	if err := catalog.ValidateClassifier(entry); err != nil {
		return nil, "", err
	}

	opts, keyEnv, err := judge.Options(entry)
	if err != nil {
		return nil, "", err
	}
	if keyEnv != "" && os.Getenv(keyEnv) == "" {
		return nil, "", fmt.Errorf("%s is not set — %s reads its key from it (by name; the value is never printed)", keyEnv, entry.Name)
	}
	c, err := tn.CreateClassifier(opts)
	if err != nil {
		return nil, "", err
	}
	label := entry.Name
	if entry.Model != "" {
		label += " (" + entry.Model + ")"
	}
	return c, label, nil
}

// flagsOf collects every value of a repeatable flag.
func flagsOf(a []string, flag string) []string {
	var out []string
	for i, s := range a {
		if s == flag && i+1 < len(a) {
			out = append(out, a[i+1])
		}
	}
	return out
}
