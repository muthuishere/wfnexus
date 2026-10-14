package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ProbePrompt is the one tiny request a probe sends. It asks for a fixed word
// so the reply is checkable without a model being clever.
const ProbePrompt = "Reply with exactly the single word PONG and nothing else."

// DoctorProbe is the result of one REAL call to the default provider. It
// exists because a PATH hit said nothing about whether the model answers:
// opencode-acp on a Zen free model was "ready" while opencode's service
// refused every call ("OpenCode's free tier can only be used from within
// OpenCode"), and every step on the default failed on turn one.
type DoctorProbe struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	OK       bool   `json:"ok"`
	Reply    string `json:"reply,omitempty"`
	Error    string `json:"error,omitempty"`
	Ms       int64  `json:"ms"`
}

// ProbeDefault makes one tiny real call to the default provider (or, with no
// default provider, to the default endpoint) and reports what came back. It
// is separate from Doctor on purpose: Doctor runs at boot and never dials a
// model; this does, so only an operator asking for it (`wfx doctor`) pays.
func (e *Engine) ProbeDefault(ctx context.Context) DoctorProbe {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	start := time.Now()
	out := e.probe(ctx)
	out.Ms = time.Since(start).Milliseconds()
	if out.Error != "" {
		out.OK = false
	}
	return out
}

func (e *Engine) probe(ctx context.Context) DoctorProbe {
	name := e.cfg.DefaultProvider
	if name == "" {
		// No default provider: steps use the default endpoint.
		p := catalog.Provider{Name: "default", Kind: catalog.KindHTTP, BaseURL: e.cfg.LLMBaseURL,
			Style: e.cfg.LLMStyle, Model: e.cfg.Model, APIKeyEnv: e.cfg.LLMAPIKeyEnv}
		return e.probeProvider(ctx, p)
	}
	p, ok := e.catalog.Providers.Get(name)
	if !ok {
		return DoctorProbe{Provider: name, Error: "not in the registry"}
	}
	return e.probeProvider(ctx, p)
}

func (e *Engine) probeProvider(ctx context.Context, p catalog.Provider) DoctorProbe {
	out := DoctorProbe{Provider: p.Name, Model: p.Model}
	sysEnv, _ := e.platformEnv(ctx, "")
	var reply string
	var err error
	switch p.Kind {
	case catalog.KindMock:
		reply = "PONG"
	case catalog.KindHTTP:
		key := ""
		if p.APIKeyEnv != "" {
			if key = os.Getenv(p.APIKeyEnv); key == "" {
				key = sysEnv[p.APIKeyEnv]
			}
			if key == "" {
				out.Error = p.APIKeyEnv + " is not set"
				return out
			}
		}
		reply, err = probeHTTP(ctx, p, key)
	case catalog.KindCLI, catalog.KindACP:
		reply, err = probeLocal(ctx, p)
	default:
		err = fmt.Errorf("unknown kind %q", p.Kind)
	}
	if err != nil {
		out.Error = oneLine(err.Error())
		return out
	}
	out.Reply = oneLine(reply)
	if strings.TrimSpace(reply) == "" {
		out.Error = "empty reply"
		return out
	}
	out.OK = true
	return out
}

// probeLocal runs one turn on the agent a step would get, in a scratch dir.
func probeLocal(ctx context.Context, p catalog.Provider) (string, error) {
	dir, err := os.MkdirTemp("", "wfx-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	pf := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(pf, []byte(ProbePrompt), 0o600); err != nil {
		return "", err
	}
	env, _ := workflow.ResolveEnv(nil)
	agent, closeAgent, err := localAgent(p, p.Model, dir, env)
	if err != nil {
		return "", err
	}
	defer closeAgent()
	return agent.Execute(ctx, devinadapter.Turn{Index: 1, Attempt: 1, Model: p.Model,
		PromptFile: pf, Prompt: ProbePrompt, Workdir: dir})
}

// probeHTTP sends one minimal chat request in the provider's style.
func probeHTTP(ctx context.Context, p catalog.Provider, key string) (string, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	var url string
	var body any
	hdr := http.Header{"Content-Type": {"application/json"}}
	if p.Style == "anthropic" {
		url = base + "/v1/messages"
		body = map[string]any{"model": p.Model, "max_tokens": 16,
			"messages": []map[string]string{{"role": "user", "content": ProbePrompt}}}
		hdr.Set("anthropic-version", "2023-06-01")
		if key != "" {
			hdr.Set("x-api-key", key)
		}
	} else {
		url = base + "/chat/completions"
		body = map[string]any{"model": p.Model, "max_tokens": 16,
			"messages": []map[string]string{{"role": "user", "content": ProbePrompt}}}
		if key != "" {
			hdr.Set("Authorization", "Bearer "+key)
		}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("unreadable reply: %w", err)
	}
	if len(r.Choices) > 0 {
		return r.Choices[0].Message.Content, nil
	}
	if len(r.Content) > 0 {
		return r.Content[0].Text, nil
	}
	return "", nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func oneLine(s string) string {
	s = strings.Join(strings.Fields(ansi.ReplaceAllString(s, "")), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
