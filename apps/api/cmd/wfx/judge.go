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
          [--backend typesafe|openrouter] [--model m] [--parallel 8] [--bands 0.30,0.70]

  questions.yaml   the shape of a workflow's decide.questions:
                     dead:
                       type: noul                   # noul | choice | score
                       instructions: Is this code unreachable at runtime?
                       true: nothing calls it, directly or indirectly
                       false: something calls it, even via reflection or a template
  --items          JSON Lines, one {"id": "...", "state": <anything>} per line; - for stdin
  --state          one state, inline (id "state")
  --backend        typesafe (TYPESAFE_API_KEY) or openrouter (OPENROUTER_API_KEY);
                   default: typesafe when TYPESAFE_API_KEY is set, else openrouter

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

	c, backend, err := judgeClassifier(flagOf(args, "--backend", ""), flagOf(args, "--model", ""))
	if err != nil {
		return err
	}

	results := make([]judgeResult, len(items))
	var cost float64
	var costKnown bool
	var mu sync.Mutex
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	start := time.Now()
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

// judgeClassifier picks the backend. The base URL and model travel with the
// backend as a unit (the library's presets): one provider's model spelling
// against another's base is the failure a hand-assembled config produces.
// WFX_JUDGE_BASE_URL overrides the base — it is how a test points this at a
// local server, and how an install behind a proxy reaches the API.
func judgeClassifier(backend, model string) (*tn.Classifier, string, error) {
	if backend == "" {
		backend = "openrouter"
		if os.Getenv("TYPESAFE_API_KEY") != "" {
			backend = "typesafe"
		}
	}
	opts := tn.ClassifierOptions{Style: tn.StyleSystemOne, Model: model, BaseURL: os.Getenv("WFX_JUDGE_BASE_URL")}
	keyEnv := ""
	switch backend {
	case "typesafe":
		opts.Backend, keyEnv = tn.BackendTypeSafe, "TYPESAFE_API_KEY"
	case "openrouter":
		opts.Backend, keyEnv = tn.BackendOpenRouter, "OPENROUTER_API_KEY"
	default:
		return nil, "", fmt.Errorf("--backend %q: want typesafe or openrouter", backend)
	}
	if os.Getenv(keyEnv) == "" {
		return nil, "", fmt.Errorf("%s is not set — the %s backend reads its key from it (by name; the value is never printed)", keyEnv, backend)
	}
	c, err := tn.CreateClassifier(opts)
	if err != nil {
		return nil, "", err
	}
	return c, backend, nil
}
