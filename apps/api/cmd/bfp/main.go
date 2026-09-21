// Command bfp drives the workflow platform from a terminal.
//
// It is a thin client over the same REST API the UI uses, so anything the CLI
// can do the UI can do and vice versa — there is no second code path and no
// second source of truth.
//
//	bfp workflows                      list workflows
//	bfp workflows show bug-fix         the full harness of every step
//	bfp apply workflows/bug-fix.yaml   validate and install a workflow
//	bfp validate workflows/x.yaml      validate without installing
//	bfp run bug-fix -i title=… -f      start a run and follow it
//	bfp runs                           recent runs
//	bfp show <run>                     a run, step by step
//	bfp logs <run> [-f]                the activity log
//	bfp approve <run> | reject <run> -m … | answer <run> -m …
//	bfp retry <run> [--step id] | cancel <run>
//	bfp registry [skills|tools|providers|classifiers|mcp]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "workflows":
		if len(rest) > 1 && rest[0] == "show" {
			return showWorkflow(rest[1])
		}
		return listWorkflows()
	case "apply":
		return apply(rest, true)
	case "validate":
		return apply(rest, false)
	case "run":
		return startRun(rest)
	case "runs":
		return listRuns()
	case "show":
		return showRun(first(rest))
	case "logs":
		return logs(rest)
	case "approve":
		return act(first(rest), "approve", nil)
	case "reject":
		return act(first(rest), "reject", map[string]any{"reason": flagOf(rest, "-m", "rejected")})
	case "answer":
		return act(first(rest), "answer", map[string]any{"answer": flagOf(rest, "-m", "")})
	case "retry":
		return act(first(rest), "retry", map[string]any{"stepId": flagOf(rest, "--step", "")})
	case "cancel":
		return act(first(rest), "cancel", nil)
	case "registry":
		return registry(first(rest))
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Print(`bfp — run agent workflows over a repository

  bfp workflows                    list workflows
  bfp workflows show <name>        every step's harness: skills, tools, team, budget, gates
  bfp apply <file.yaml>            validate and install a workflow
  bfp validate <file.yaml>         validate only; writes nothing
  bfp run <workflow> -i k=v [-f]   start a run (-f follows the log)
  bfp runs                         recent runs
  bfp show <run-id>                a run, step by step
  bfp logs <run-id> [-f]           the activity log
  bfp approve <run-id>             approve the step waiting on a human
  bfp reject <run-id> -m "why"     reject it
  bfp answer <run-id> -m "text"    answer an agent's question
  bfp retry <run-id> [--step id]   re-run from a step
  bfp cancel <run-id>
  bfp registry [skills|tools|providers|classifiers|mcp]

The API is $BFP_API (default http://127.0.0.1:8090). Add --json to any
listing for machine-readable output.
`)
}

// ---- transport -------------------------------------------------------------

func base() string {
	if v := os.Getenv("BFP_API"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8090"
}

func call(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s — is the API running? (BFP_API=%s)", err, base())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ---- workflows -------------------------------------------------------------

type step struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Skills      []string `json:"skills"`
	Tools       []string `json:"tools"`
	MCP         []string `json:"mcp"`
	Needs       []string `json:"needs"`
	Provider    string   `json:"provider"`
	Classifier  string   `json:"classifier"`
	RequiresApp bool     `json:"requiresApproval"`
	AskHuman    bool     `json:"askHuman"`
	Team        []struct {
		ID   string `json:"id"`
		Does string `json:"does"`
	} `json:"team"`
	Guardrails []struct {
		Deny   string `json:"deny"`
		Reason string `json:"reason"`
	} `json:"guardrails"`
	Decide *struct {
		Questions map[string]any `json:"questions"`
	} `json:"decide"`
	OutputSchema map[string]any `json:"outputSchema"`
}

type wf struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	MaxParallel int    `json:"maxParallel"`
	Steps       []step `json:"steps"`
	Path        string `json:"path"`
}

func listWorkflows() error {
	var out []wf
	if err := call("GET", "/api/workflows", nil, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("no workflows — add one under workflows/ or `bfp apply <file>`")
		return nil
	}
	for _, w := range out {
		mode := "sequential"
		for _, s := range w.Steps {
			if len(s.Needs) > 0 {
				mode = fmt.Sprintf("parallel (max %d)", w.MaxParallel)
				break
			}
		}
		fmt.Printf("%-16s %d steps  %-20s %s\n", w.Name, len(w.Steps), mode, firstLine(w.Description))
	}
	return nil
}

func showWorkflow(name string) error {
	var w wf
	if err := call("GET", "/api/workflows/"+name, nil, &w); err != nil {
		return err
	}
	fmt.Printf("%s — %s\n%s\n\n", w.Name, firstLine(w.Description), w.Path)
	for i, s := range w.Steps {
		marks := []string{}
		if s.RequiresApp {
			marks = append(marks, "approval gate")
		}
		if s.AskHuman {
			marks = append(marks, "may ask a human")
		}
		if s.Decide != nil {
			marks = append(marks, fmt.Sprintf("judge (%d questions)", len(s.Decide.Questions)))
		}
		suffix := ""
		if len(marks) > 0 {
			suffix = "  [" + strings.Join(marks, ", ") + "]"
		}
		fmt.Printf("%d. %s%s\n", i+1, or(s.Name, s.ID), suffix)
		if len(s.Needs) > 0 {
			fmt.Printf("     needs      %s\n", strings.Join(s.Needs, ", "))
		}
		line("skills", s.Skills)
		line("tools", s.Tools)
		line("mcp", s.MCP)
		if s.Provider != "" {
			fmt.Printf("     provider   %s\n", s.Provider)
		}
		if s.Classifier != "" {
			fmt.Printf("     classifier %s\n", s.Classifier)
		}
		for _, m := range s.Team {
			fmt.Printf("     sub-agent  %s — %s\n", m.ID, firstLine(m.Does))
		}
		for _, g := range s.Guardrails {
			fmt.Printf("     denies     %-12s %s\n", g.Deny, firstLine(g.Reason))
		}
		if req, ok := s.OutputSchema["required"].([]any); ok && len(req) > 0 {
			fmt.Printf("     must emit  %s\n", strings.Join(toStrings(req), ", "))
		}
		fmt.Println()
	}
	return nil
}

func line(label string, vs []string) {
	if len(vs) > 0 {
		fmt.Printf("     %-10s %s\n", label, strings.Join(vs, ", "))
	}
}

// apply validates a YAML workflow and, when install is true, installs it.
func apply(args []string, install bool) error {
	path := first(args)
	if path == "" {
		return fmt.Errorf("usage: bfp %s <file.yaml>", map[bool]string{true: "apply", false: "validate"}[install])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Decode through the REAL definition type rather than a generic map: the
	// YAML keys are snake_case and the API speaks the JSON names, so a map
	// round trip silently drops every multi-word field (output_schema,
	// requires_approval, max_turns…). Going through the struct means the two
	// spellings are the same field by construction.
	var def workflow.Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	name := def.Name
	if name == "" {
		return fmt.Errorf("%s: the workflow needs a name", path)
	}
	if !install {
		var res struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		}
		if err := call("POST", "/api/workflows/validate", map[string]any{"definition": def}, &res); err != nil {
			return err
		}
		if !res.Valid {
			return fmt.Errorf("%s", res.Error)
		}
		fmt.Printf("%s is valid\n", name)
		return nil
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := call("PUT", "/api/workflows/"+name, map[string]any{"definition": def}, &res); err != nil {
		return err
	}
	fmt.Printf("installed %s → %s\n", name, res.Path)
	return nil
}

// ---- runs ------------------------------------------------------------------

type runRow struct {
	ID          string         `json:"id"`
	Workflow    string         `json:"workflow"`
	Status      string         `json:"status"`
	CurrentStep string         `json:"currentStep"`
	Error       string         `json:"error"`
	Input       map[string]any `json:"input"`
	CreatedAt   time.Time      `json:"createdAt"`
}

func startRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bfp run <workflow> -i key=value [-f]")
	}
	name := args[0]
	input := map[string]any{}
	for i := 1; i < len(args); i++ {
		if (args[i] == "-i" || args[i] == "--input") && i+1 < len(args) {
			k, v, ok := strings.Cut(args[i+1], "=")
			if !ok {
				return fmt.Errorf("input must be key=value, got %q", args[i+1])
			}
			input[k] = v
			i++
		}
	}
	var r runRow
	if err := call("POST", "/api/workflows/"+name+"/runs", input, &r); err != nil {
		return err
	}
	fmt.Printf("run %s started (%s)\n", r.ID, name)
	if has(args, "-f") || has(args, "--follow") {
		return follow(r.ID)
	}
	fmt.Printf("follow it with:  bfp logs %s -f\n", r.ID)
	return nil
}

func listRuns() error {
	var rows []runRow
	if err := call("GET", "/api/runs", nil, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no runs yet")
		return nil
	}
	for _, r := range rows {
		title, _ := r.Input["title"].(string)
		if title == "" {
			title = r.ID[:8]
		}
		fmt.Printf("%-36s %-14s %-18s %-14s %s\n", r.ID, r.Workflow, r.Status, r.CurrentStep, firstLine(title))
	}
	return nil
}

func showRun(id string) error {
	if id == "" {
		return fmt.Errorf("usage: bfp show <run-id>")
	}
	var d struct {
		Run   runRow `json:"run"`
		Steps []struct {
			StepID   string `json:"stepId"`
			Status   string `json:"status"`
			Turns    int    `json:"turns"`
			Attempts int    `json:"attempts"`
			Error    string `json:"error"`
		} `json:"steps"`
		Artifacts []struct {
			StepID string `json:"stepId"`
			Name   string `json:"name"`
			Size   int64  `json:"sizeBytes"`
		} `json:"artifacts"`
	}
	if err := call("GET", "/api/runs/"+id, nil, &d); err != nil {
		return err
	}
	fmt.Printf("%s  %s  [%s]\n", d.Run.ID, d.Run.Workflow, d.Run.Status)
	if d.Run.Error != "" {
		fmt.Printf("  %s\n", d.Run.Error)
	}
	fmt.Println()
	for _, s := range d.Steps {
		mark := map[string]string{"done": "✓", "failed": "✗", "running": "▸", "skipped": "–"}[s.Status]
		if mark == "" {
			mark = "·"
		}
		fmt.Printf(" %s %-16s %-18s turns=%-3d %s\n", mark, s.StepID, s.Status, s.Turns, firstLine(s.Error))
	}
	if len(d.Artifacts) > 0 {
		fmt.Println("\nartifacts:")
		for _, a := range d.Artifacts {
			fmt.Printf("  %s/%s  %.1f KB\n", a.StepID, a.Name, float64(a.Size)/1024)
		}
	}
	switch d.Run.Status {
	case "awaiting_approval":
		fmt.Printf("\nwaiting for you:  bfp approve %s   (or reject -m \"why\")\n", id)
	case "needs_input":
		fmt.Printf("\nwaiting for you:  bfp answer %s -m \"…\"\n", id)
	}
	return nil
}

func logs(args []string) error {
	id := first(args)
	if id == "" {
		return fmt.Errorf("usage: bfp logs <run-id> [-f]")
	}
	if has(args, "-f") || has(args, "--follow") {
		return follow(id)
	}
	return follow(id) // the stream replays the backlog, then ends when cancelled
}

// follow streams the run's activity. The SSE endpoint replays everything that
// already happened before live events, so a late follower sees the whole run.
func follow(id string) error {
	res, err := http.Get(base() + "/api/runs/" + id + "/events?after=0")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			StepID  string         `json:"stepId"`
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		if s := render(ev.StepID, ev.Kind, ev.Payload); s != "" {
			fmt.Println(s)
		}
		if ev.Kind == "run.status" {
			switch ev.Payload["status"] {
			case "done", "failed", "cancelled", "awaiting_approval", "needs_input":
				return nil
			}
		}
	}
	return sc.Err()
}

func render(stepID, kind string, p map[string]any) string {
	at := fmt.Sprintf("%-14s", stepID)
	str := func(k string) string { s, _ := p[k].(string); return s }
	switch kind {
	case "run.status":
		return fmt.Sprintf("%s run → %s %s", at, str("status"), str("error"))
	case "step.status":
		return fmt.Sprintf("%s step → %s %s", at, str("status"), str("error"))
	case "tool_call":
		args, _ := json.Marshal(p["args"])
		return fmt.Sprintf("%s → %s %s", at, str("name"), clip(string(args), 140))
	case "tool_result":
		flag := ""
		if b, _ := p["isError"].(bool); b {
			flag = " (error)"
		}
		return fmt.Sprintf("%s ← %s%s %s", at, str("name"), flag, clip(str("output"), 200))
	case "llm":
		if t := str("text"); t != "" {
			return fmt.Sprintf("%s   %s", at, clip(t, 300))
		}
	case "decision":
		raw, _ := json.Marshal(p["answers"])
		return fmt.Sprintf("%s judge %s", at, clip(string(raw), 200))
	case "log", "error":
		return fmt.Sprintf("%s   %s", at, clip(str("text"), 300))
	case "artifact":
		return fmt.Sprintf("%s artifact %s", at, str("name"))
	}
	return ""
}

func act(id, verb string, body map[string]any) error {
	if id == "" {
		return fmt.Errorf("usage: bfp %s <run-id>", verb)
	}
	if body == nil {
		body = map[string]any{}
	}
	if err := call("POST", "/api/runs/"+id+"/"+verb, body, nil); err != nil {
		return err
	}
	fmt.Printf("%s ok\n", verb)
	return nil
}

// ---- registries ------------------------------------------------------------

func registry(kind string) error {
	if kind == "" {
		kind = "all"
	}
	if kind == "tools" {
		var tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := call("GET", "/api/tools", nil, &tools); err != nil {
			return err
		}
		for _, t := range tools {
			fmt.Printf("%-14s %s\n", t.Name, firstLine(t.Description))
		}
		return nil
	}
	if kind == "skills" {
		var d struct {
			Skills []struct {
				Name, Description, Source string
			} `json:"skills"`
			Skipped []struct{ Location, Reason string } `json:"skipped"`
		}
		if err := call("GET", "/api/skills", nil, &d); err != nil {
			return err
		}
		sort.Slice(d.Skills, func(i, j int) bool { return d.Skills[i].Name < d.Skills[j].Name })
		for _, s := range d.Skills {
			fmt.Printf("%-22s %-8s %s\n", s.Name, s.Source, firstLine(s.Description))
		}
		if n := len(d.Skipped); n > 0 {
			fmt.Printf("\n%d skipped (a skill that does not parse is invisible — `bfp registry skills --json` for the list)\n", n)
		}
		return nil
	}
	path := map[string]string{"providers": "/api/providers", "classifiers": "/api/classifiers", "mcp": "/api/mcp", "all": "/api/registries"}[kind]
	if path == "" {
		return fmt.Errorf("unknown registry %q (skills, tools, providers, classifiers, mcp)", kind)
	}
	var raw json.RawMessage
	if err := call("GET", path, nil, &raw); err != nil {
		return err
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, raw, "", "  ")
	fmt.Println(pretty.String())
	return nil
}

// ---- helpers ---------------------------------------------------------------

func first(a []string) string {
	for _, s := range a {
		if !strings.HasPrefix(s, "-") {
			return s
		}
	}
	return ""
}

func has(a []string, flag string) bool {
	for _, s := range a {
		if s == flag {
			return true
		}
	}
	return false
}

func flagOf(a []string, flag, def string) string {
	for i, s := range a {
		if s == flag && i+1 < len(a) {
			return a[i+1]
		}
	}
	return def
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 90)
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ⏎ ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func toStrings(v []any) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		out = append(out, fmt.Sprint(x))
	}
	return out
}
