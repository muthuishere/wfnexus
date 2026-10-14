package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The quiet view of a run: one line per thing a person cares about. `-v` keeps
// the full transcript (render). Display only — the events, the API and the UI
// carry everything unchanged.

// shellPreamble is what the engine puts in front of every `bash` command: the
// step's secrets file (`. '/…/secrets/step.env' && `) and its env
// (`export K='v' K2="${REF}" && `). Neither is what the agent asked to run.
var shellPreamble = regexp.MustCompile(
	`^\s*(?:\.\s+(?:'[^']*'|"[^"]*"|\S+)\s*&&\s*)?` +
		`(?:export\s+(?:[A-Za-z_][A-Za-z0-9_]*=(?:'(?:[^']|'\\'')*'|"[^"]*"|\S*)\s+)+&&\s*)?` +
		`(?:WFX_[A-Z0-9_]*=(?:'[^']*'|"[^"]*"|\S*)\s+)*`)

// secretsPath hides any run-local secrets file that is still in the text.
var secretsPath = regexp.MustCompile(`'?[^\s']*/secrets/[^\s']+\.env'?`)

// readableCommand is the command the agent wrote, without the env preamble.
func readableCommand(c string) string {
	return secretsPath.ReplaceAllString(shellPreamble.ReplaceAllString(c, ""), "<secrets>")
}

// toolArg is the one human-sized argument that says what a tool call did.
func toolArg(name string, args any) string {
	m, _ := args.(map[string]any)
	s := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	var out string
	switch name {
	case "bash", "shell", "run_shell_command":
		out = readableCommand(s("command", "cmd"))
	case "submit_output":
		return ""
	case "task":
		out = s("agent", "subagent_type", "id", "description")
	default:
		out = s("file_path", "path", "pattern", "url", "query", "command", "name")
		if out == "" && len(m) > 0 {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if v, ok := m[k].(string); ok && v != "" {
					out = v
					break
				}
			}
		}
	}
	return clip(secretsPath.ReplaceAllString(out, "<secrets>"), 90)
}

// stepSummary is what the run's detail says about one step, fetched when it ends.
type stepSummary struct {
	Turns   int
	Elapsed time.Duration
	Cost    *float64
	Output  json.RawMessage
}

type quietView struct {
	runID   string
	base    string
	lookup  func(stepID string) (stepSummary, bool)
	last    string                 // last step with output, for the final answer
	outputs map[string]json.RawMessage
	arts    []string
}

func (q *quietView) line(stepID, kind string, p map[string]any) []string {
	str := func(k string) string { s, _ := p[k].(string); return s }
	switch kind {
	case "step.status":
		switch st := str("status"); st {
		case "running":
			return []string{"▸ " + stepID}
		case "done":
			l := "✓ " + stepID
			if q.lookup != nil {
				if s, ok := q.lookup(stepID); ok {
					var bits []string
					if s.Turns > 0 {
						bits = append(bits, fmt.Sprintf("%d turns", s.Turns))
					}
					if s.Elapsed > 0 {
						bits = append(bits, s.Elapsed.Round(time.Second).String())
					}
					if s.Cost != nil {
						bits = append(bits, fmt.Sprintf("$%.2f", *s.Cost))
					}
					if len(bits) > 0 {
						l += "  " + strings.Join(bits, " · ")
					}
					if len(s.Output) > 0 && string(s.Output) != "null" {
						if q.outputs == nil {
							q.outputs = map[string]json.RawMessage{}
						}
						q.outputs[stepID] = s.Output
						q.last = stepID
					}
				}
			}
			return []string{l}
		case "failed":
			return []string{"✗ " + stepID + "  " + firstLine(str("error"))}
		case "pending", "queued":
			return nil
		default:
			return []string{"‖ " + stepID + "  " + st}
		}
	case "tool_call":
		name := str("name")
		if name == "submit_output" {
			return []string{"  · submit"}
		}
		if a := toolArg(name, p["args"]); a != "" {
			return []string{"  · " + name + "  " + a}
		}
		return []string{"  · " + name}
	case "tool_result":
		if b, _ := p["isError"].(bool); b {
			if str("name") == "submit_output" {
				return []string{"    ✗ output rejected — the step retries"}
			}
			return []string{"    ✗ " + firstLine(secretsPath.ReplaceAllString(str("output"), "<secrets>"))}
		}
	case "decision":
		return []string{"  · judge  " + compactJSON(p["answers"], 90)}
	case "error":
		return []string{"  ! " + firstLine(str("text"))}
	case "artifact":
		ref := str("name")
		if s, _ := p["sizeBytes"].(float64); s > 0 {
			ref += fmt.Sprintf(" (%.1f KB)", s/1024)
		}
		if id := str("id"); id != "" && q.base != "" {
			ref += "  " + q.base + "/api/runs/" + q.runID + "/artifacts/" + id
		}
		q.arts = append(q.arts, ref)
		return []string{"  · artifact " + ref}
	case "run.status":
		st := str("status")
		var out []string
		if st == "done" && q.last != "" {
			out = append(out, "", "result: "+resultOf(q.outputs[q.last]))
		}
		switch st {
		case "done", "failed", "cancelled":
			if len(q.arts) > 0 {
				out = append(out, "artifacts:")
				for _, a := range q.arts {
					out = append(out, "  "+a)
				}
			}
			l := "run " + st
			if e := str("error"); e != "" {
				l += "  " + firstLine(e)
			}
			out = append(out, l+"   (wfx show "+q.runID+")")
		case "awaiting_approval":
			out = append(out, "‖ waiting for you:  wfx approve "+q.runID+"   (or reject -m \"why\")")
		case "needs_input":
			out = append(out, "‖ waiting for you:  wfx answer "+q.runID+" -m \"…\"")
		}
		return out
	}
	return nil
}

// resultOf picks the field a person reads first from a step's output.
func resultOf(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"answer", "result", "summary", "verdict", "message", "text"} {
			if v, ok := m[k].(string); ok && v != "" {
				return clip(v, 300)
			}
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clip(s, 300)
	}
	return compactJSON(json.RawMessage(raw), 300)
}

// compactJSON without HTML escaping: `&&` stays `&&`, not `\u0026\u0026`.
func compactJSON(v any, n int) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return clip(b.String(), n)
}

// runStepLookup reads the run's detail for a step's turns, time, cost and output.
func runStepLookup(runID string) func(string) (stepSummary, bool) {
	return func(stepID string) (stepSummary, bool) {
		var d struct {
			Steps []struct {
				StepID     string          `json:"stepId"`
				Turns      int             `json:"turns"`
				Output     json.RawMessage `json:"output"`
				Usage      json.RawMessage `json:"usage"`
				StartedAt  *time.Time      `json:"startedAt"`
				FinishedAt *time.Time      `json:"finishedAt"`
			} `json:"steps"`
		}
		if call("GET", "/api/runs/"+runID, nil, &d) != nil {
			return stepSummary{}, false
		}
		for _, s := range d.Steps {
			if s.StepID != stepID {
				continue
			}
			out := stepSummary{Turns: s.Turns, Output: s.Output}
			var u struct {
				Cost      *float64 `json:"costUsd"`
				ElapsedMs int64    `json:"elapsedMs"`
			}
			_ = json.Unmarshal(s.Usage, &u)
			out.Cost = u.Cost
			if s.StartedAt != nil && s.FinishedAt != nil {
				out.Elapsed = s.FinishedAt.Sub(*s.StartedAt)
			} else if u.ElapsedMs > 0 {
				out.Elapsed = time.Duration(u.ElapsedMs) * time.Millisecond
			}
			return out, true
		}
		return stepSummary{}, false
	}
}
