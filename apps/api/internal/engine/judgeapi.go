package engine

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// JudgeItem is one state to classify, as `wfx judge --items` reads it.
type JudgeItem struct {
	ID    string `json:"id"`
	State any    `json:"state"`
}

// JudgeResult is one item's verdict, the same shape `wfx judge` prints.
type JudgeResult struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model,omitempty"`
	Calibrated bool                    `json:"calibrated"`
	Answers    map[string]judge.Answer `json:"answers,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

// Judge answers typed questions about each item on a classifier from THIS
// server's registry — the entry a step would get from `classifier: <name>`,
// or the operator's default when name is empty.
//
// It is how `wfx judge` run inside a step reaches the registry: steps do not
// inherit the server's configuration (WFX_REGISTRIES and the classifier key
// are scrubbed from what runs see, and must stay so), so instead of handing a
// step the file paths and the key, the step asks the server and the key never
// leaves this process.
func (e *Engine) Judge(ctx context.Context, name string, questions map[string]workflow.Question, items []JudgeItem, bands judge.Bands, parallel int) ([]JudgeResult, *float64, error) {
	tq, err := judge.Questions(questions)
	if err != nil {
		return nil, nil, err
	}
	c, err := e.classifierFor(&workflow.Step{Classifier: name})
	if err != nil {
		return nil, nil, err
	}
	if parallel < 1 {
		parallel = 1
	}
	results := make([]JudgeResult, len(items))
	var cost float64
	var costKnown bool
	var mu sync.Mutex
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it JudgeItem) {
			defer wg.Done()
			defer func() { <-sem }()
			r := JudgeResult{ID: it.ID}
			d, err := c.Evaluate(ctx, it.State, tq)
			if err == nil {
				r.Model, r.Calibrated = d.Model, d.Calibrated
				r.Answers, err = judge.Read(d, questions, bands)
				if d.Usage.Cost != nil {
					mu.Lock()
					cost += *d.Usage.Cost
					costKnown = true
					mu.Unlock()
				}
			}
			if err != nil {
				r.Error = scrub(err.Error())
			}
			results[i] = r
		}(i, it)
	}
	wg.Wait()
	if !costKnown {
		return results, nil, nil
	}
	return results, &cost, nil
}

// SelfURL is how a process on THIS machine reaches this server: the listen
// address with an unspecified host turned into loopback. It is deliberately
// not WFX_PUBLIC_URL — that is the address workers outside use, and inside a
// container it names the host's port mapping, which the container itself
// cannot reach.
func (e *Engine) SelfURL() string {
	return selfURL(e.cfg.Addr)
}

func selfURL(addr string) string {
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port
}
