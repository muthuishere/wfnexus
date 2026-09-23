// Command wfx drives the workflow platform from a terminal.
//
// It is a thin client over the same REST API the UI uses, so anything the CLI
// can do the UI can do and vice versa — there is no second code path and no
// second source of truth.
//
//	wfx workflows                      list workflows
//	wfx workflows show bug-fix         the full harness of every step
//	wfx apply workflows/bug-fix.yaml   validate and install a workflow
//	wfx validate workflows/x.yaml      validate without installing
//	wfx run bug-fix -i title=… -f      start a run and follow it
//	wfx runs                           recent runs
//	wfx show <run>                     a run, step by step
//	wfx logs <run> [-f]                the activity log
//	wfx approve <run> | reject <run> -m … | answer <run> -m …
//	wfx retry <run> [--step id] | cancel <run>
//	wfx registry [skills|tools|providers|classifiers|mcp]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
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
		if len(rest) >= 2 && rest[0] == "--from-run" {
			return applyFromRun(rest[1], flagOf(rest, "--as", ""))
		}
		return apply(rest, true)
	case "validate":
		return apply(rest, false)
	case "run":
		return startRun(rest)
	case "runs":
		return listRuns(rest...)
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
	case "doctor":
		return doctor()
	case "dryrun":
		return dryRun(rest)
	case "import":
		return importRepo(rest)
	case "projects", "project":
		return projects(rest)
	case "sources":
		return sources(rest)
	case "workers", "worker":
		return workers(rest)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Print(`wfx — run agent workflows over a repository

  wfx workflows                    list workflows
  wfx workflows show <name>        every step's harness: skills, tools, team, budget, gates
  wfx apply <file.yaml>            validate and install a workflow
  wfx apply --from-run <id>        install the workflow a run authored (validated first)
  wfx validate <file.yaml>         validate only; writes nothing
  wfx run <workflow> -i k=v [-f]   start a run (-f follows the log)
  wfx runs [--project p] [--workflow w]  recent runs, newest first
  wfx workers                      machines in the pool, and the line that adds another
  wfx workers rm <id>              forget a machine
  wfx workers rotate               new join token; machines already joined keep working
  wfx show <run-id>                a run, step by step
  wfx logs <run-id> [-f]           the activity log
  wfx approve <run-id>             approve the step waiting on a human
  wfx reject <run-id> -m "why"     reject it
  wfx answer <run-id> -m "text"    answer an agent's question
  wfx dryrun <workflow> [-i k=v]   would it run here? no model, no repo, no writes
  wfx projects                     every project: workflows and run activity
  wfx project add <repo> [--as n]  add a project — a repo whose .wfx/workflows/ we run
  wfx project rm <name>            forget one (the clone stays on disk)
  wfx sources [forget <name>]      where workflows are loaded from
  wfx doctor                       what is wired: default model, providers, classifiers, skills
  wfx retry <run-id> [--step id]   re-run from a step
  wfx cancel <run-id>
  wfx registry [skills|tools|providers|classifiers|mcp]

The API is $WFX_API (default http://127.0.0.1:8090). Add --json to any
listing for machine-readable output.
`)
}

// ---- transport -------------------------------------------------------------

func base() string {
	if v := os.Getenv("WFX_API"); v != "" {
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
		return fmt.Errorf("%s — is the API running? (WFX_API=%s)", err, base())
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
		fmt.Println("no workflows — add one under workflows/ or `wfx apply <file>`")
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
		return fmt.Errorf("usage: wfx %s <file.yaml>", map[bool]string{true: "apply", false: "validate"}[install])
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
	Project     string         `json:"project"`
	Workflow    string         `json:"workflow"`
	Status      string         `json:"status"`
	CurrentStep string         `json:"currentStep"`
	Error       string         `json:"error"`
	Input       map[string]any `json:"input"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// inputsFrom parses repeated `-i key=value`, shared by `run` and `dryrun` so
// the two cannot drift in how they read a command line.
func inputsFrom(args []string) (map[string]any, error) {
	input := map[string]any{}
	for i := 0; i < len(args); i++ {
		if (args[i] == "-i" || args[i] == "--input") && i+1 < len(args) {
			k, v, ok := strings.Cut(args[i+1], "=")
			if !ok {
				return nil, fmt.Errorf("input must be key=value, got %q", args[i+1])
			}
			input[k] = v
			i++
		}
	}
	return input, nil
}

func startRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wfx run <workflow> -i key=value [-f]")
	}
	name := args[0]
	input, err := inputsFrom(args[1:])
	if err != nil {
		return err
	}
	var r runRow
	if err := call("POST", "/api/workflows/"+name+"/runs", input, &r); err != nil {
		return err
	}
	fmt.Printf("run %s started (%s)\n", r.ID, name)
	if has(args, "-f") || has(args, "--follow") {
		return follow(r.ID)
	}
	fmt.Printf("follow it with:  wfx logs %s -f\n", r.ID)
	return nil
}

func listRuns(args ...string) error {
	// Narrowed by project and workflow, the two axes of project → workflow →
	// runs. A flat global list stops meaning anything with a second repository.
	q := url.Values{}
	if p := flagOf(args, "--project", ""); p != "" {
		q.Set("project", p)
	}
	if w := flagOf(args, "--workflow", ""); w != "" {
		q.Set("workflow", w)
	}
	path := "/api/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rows []runRow
	if err := call("GET", path, nil, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no runs yet")
		return nil
	}
	fmt.Printf("%-36s %-12s %-14s %-16s %-12s %s\n", "RUN", "PROJECT", "WORKFLOW", "STATUS", "STEP", "WHAT")
	for _, r := range rows {
		title, _ := r.Input["title"].(string)
		if title == "" {
			title = r.ID[:8]
		}
		fmt.Printf("%-36s %-12s %-14s %-16s %-12s %s\n",
			r.ID, r.Project, r.Workflow, r.Status, r.CurrentStep, firstLine(title))
	}
	return nil
}

func showRun(id string) error {
	if id == "" {
		return fmt.Errorf("usage: wfx show <run-id>")
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
		fmt.Printf("\nwaiting for you:  wfx approve %s   (or reject -m \"why\")\n", id)
	case "needs_input":
		fmt.Printf("\nwaiting for you:  wfx answer %s -m \"…\"\n", id)
	}
	return nil
}

func logs(args []string) error {
	id := first(args)
	if id == "" {
		return fmt.Errorf("usage: wfx logs <run-id> [-f]")
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
		return fmt.Errorf("usage: wfx %s <run-id>", verb)
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

// doctor prints what is actually wired on this machine, as opposed to what the
// registries declare. Every entry is a name (ADR 0011), and a name resolves
// against this machine: an env var that may not be set, a CLI that may not be
// installed. That used to be discoverable only by starting a run.
//
// It prints the NAME of a key variable and whether it is set. Never a value.
// projects is the top of the hierarchy: project → workflow → runs, the same
// shape GitHub Actions has. A run belongs to the repository it acted on, and
// "which repo was that" is the first question anyone asks about a run.
func projects(args []string) error {
	switch {
	case len(args) >= 2 && (args[0] == "add" || args[0] == "import"):
		return importRepo(args[1:])
	case len(args) >= 2 && (args[0] == "rm" || args[0] == "remove" || args[0] == "forget"):
		if err := call("DELETE", "/api/projects/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("forgot %s (the clone is left on disk)\n", args[1])
		return nil
	}

	var list []struct {
		Name, Dir, Repo, URL, LastRun, LastRunAt string
		Local                                    bool
		Workflows                                []string
		Runs                                     int
		Problems                                 []struct{ Location, Reason string }
	}
	if err := call("GET", "/api/projects", nil, &list); err != nil {
		return err
	}
	fmt.Printf("%-16s %-9s %-6s %-12s %s\n", "PROJECT", "WORKFLOWS", "RUNS", "LAST RUN", "WHERE")
	for _, p := range list {
		where := p.Repo
		if p.URL != "" {
			where = p.URL
		}
		if p.Local {
			where = p.Dir + "  (this platform's own)"
		}
		last := p.LastRun
		if last == "" {
			last = "—"
		}
		fmt.Printf("%-16s %-9d %-6d %-12s %s\n", p.Name, len(p.Workflows), p.Runs, last, where)
		for _, pr := range p.Problems {
			fmt.Printf("  ✗ %s: %s\n", pr.Location, pr.Reason)
		}
	}
	return nil
}

// dryRun answers "would this actually work here?" before anything is spent.
// Validation asks whether the FILE is well formed; this asks whether THIS
// MACHINE can run it, which is the question almost every real failure turned
// out to be.
func dryRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wfx dryrun <workflow> [-i key=value]")
	}
	input, err := inputsFrom(args[1:])
	if err != nil {
		return err
	}
	var d struct {
		Workflow string
		OK       bool
		Shape    string
		Waves    [][]string
		Steps    []struct {
			ID, Kind, Provider, Model, Shell, Prompt, RunsOn string
			Skills, Tools                                    []string
			MaxTurns, Wave, Workers                          int
		}
		Problems []struct {
			Step, Field, Message string
			Fatal                bool
		}
		Cost struct {
			MaxTurns, MaxWallSec, AgentSteps, FreeSteps int
			MaxToolCalls                                int64
		}
	}
	if err := call("POST", "/api/workflows/"+url.PathEscape(args[0])+"/dryrun", input, &d); err != nil {
		return err
	}

	fmt.Printf("%s — %s, %d steps\n\n", d.Workflow, d.Shape, len(d.Steps))
	for i, wave := range d.Waves {
		lead := fmt.Sprintf("wave %d", i+1)
		if len(wave) > 1 {
			lead += fmt.Sprintf(" (%d at once)", len(wave))
		}
		fmt.Printf("%-16s %s\n", lead, strings.Join(wave, ", "))
	}

	fmt.Printf("\n%-16s %-7s %-28s %s\n", "STEP", "KIND", "RUNS ON", "BUDGET")
	for _, s := range d.Steps {
		on := s.Model
		if s.Kind == "run" {
			// Where it runs is the label when it is placed on a machine, and
			// the interpreter when it runs here. Both answer the same question.
			on = s.Shell
			if s.RunsOn != "" {
				on = s.RunsOn + " (" + plural(s.Workers, "machine") + ")"
			}
		}
		budget := "—"
		if s.MaxTurns > 0 {
			budget = fmt.Sprintf("%d turns", s.MaxTurns)
		}
		fmt.Printf("%-16s %-7s %-28s %s\n", s.ID, s.Kind, on, budget)
	}

	fmt.Printf("\nceiling: %d model turns across %d agent step(s)", d.Cost.MaxTurns, d.Cost.AgentSteps)
	if d.Cost.FreeSteps > 0 {
		fmt.Printf("; %d step(s) call no model", d.Cost.FreeSteps)
	}
	fmt.Println()

	if len(d.Problems) > 0 {
		fmt.Printf("\n%d problem(s):\n", len(d.Problems))
		for _, p := range d.Problems {
			mark, where := "warning", p.Step
			if p.Fatal {
				mark = "FATAL  "
			}
			if where != "" && p.Field != "" {
				where += "." + p.Field
			}
			fmt.Printf("  %s %-22s %s\n", mark, where, p.Message)
		}
	}
	if !d.OK {
		return fmt.Errorf("this workflow would not run here")
	}
	fmt.Println("\nwould run.")
	return nil
}

// importRepo registers a repository as a workflow source. A workflow lives in
// the repository it acts on — the same arrangement as .github/workflows — so
// this is how one arrives from outside.
func importRepo(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wfx import <repo-url-or-path> [--as name] [--branch b]")
	}
	body := map[string]any{
		"repo":   args[0],
		"name":   flagOf(args, "--as", ""),
		"branch": flagOf(args, "--branch", ""),
	}
	var src struct{ Name, Dir, Repo, URL string }
	if err := call("POST", "/api/sources", body, &src); err != nil {
		return err
	}
	fmt.Printf("imported %s from %s\n", src.Name, src.Dir)
	return listWorkflows()
}

func sources(args []string) error {
	if len(args) >= 2 && args[0] == "forget" {
		if err := call("DELETE", "/api/sources/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Printf("forgot %s (the clone is left on disk)\n", args[1])
		return nil
	}
	var d struct {
		Sources []struct{ Name, Dir, Repo, URL string }
		Skipped []struct{ Source, Location, Reason string }
	}
	if err := call("GET", "/api/sources", nil, &d); err != nil {
		return err
	}
	for _, s := range d.Sources {
		where := s.Dir
		if s.URL != "" {
			where = s.URL + "  → " + s.Dir
		}
		fmt.Printf("%-16s %s\n", s.Name, where)
	}
	for _, sk := range d.Skipped {
		fmt.Printf("  ✗ %s: %s\n", sk.Location, sk.Reason)
	}
	return nil
}

// applyFromRun installs a workflow that `author-workflow` produced.
//
// It exists because an agent's own account of having checked its work is not
// evidence. A live authoring run reported `dry_run_clean: true` and submitted a
// definition that the loader refused — not because the dry run was wrong, but
// because the definition submitted was not the one last dry-run. The platform
// therefore re-checks here, against the same validation every saved workflow
// passes, and the agent's boolean is treated as a claim rather than a fact.
func applyFromRun(runID, as string) error {
	var d struct {
		Steps []struct {
			StepID string         `json:"stepId"`
			Output map[string]any `json:"output"`
		} `json:"steps"`
	}
	if err := call("GET", "/api/runs/"+runID, nil, &d); err != nil {
		return err
	}
	var def map[string]any
	for _, s := range d.Steps {
		if v, ok := s.Output["definition"].(map[string]any); ok {
			def = v
		}
	}
	if def == nil {
		return fmt.Errorf("run %s produced no `definition` — is it an author-workflow run, and did it finish?", runID)
	}
	name, _ := def["name"].(string)
	if as != "" {
		name, def["name"] = as, as
	}
	if name == "" {
		return fmt.Errorf("the definition has no name; pass --as <name>")
	}

	// Checked BEFORE saving, and the result is reported whether or not it
	// passes — the point is that this verdict comes from the platform.
	var dry struct {
		OK       bool
		Problems []struct {
			Step, Field, Message string
			Fatal                bool
		}
	}
	if err := call("POST", "/api/dryrun", map[string]any{"definition": def}, &dry); err != nil {
		return err
	}
	if !dry.OK {
		fmt.Printf("%s was NOT installed — the dry run found:\n", name)
		for _, p := range dry.Problems {
			if p.Fatal {
				fmt.Printf("  FATAL %s %s: %s\n", p.Step, p.Field, p.Message)
			}
		}
		return fmt.Errorf("fix the workflow, or re-run author-workflow saying what to change")
	}

	var saved struct{ Path string }
	if err := call("PUT", "/api/workflows/"+url.PathEscape(name), map[string]any{"definition": def}, &saved); err != nil {
		return err
	}
	fmt.Printf("installed %s → %s\n", name, saved.Path)
	fmt.Printf("dry run clean. try it with:  wfx dryrun %s\n", name)
	return nil
}

func doctor() error {
	var d struct {
		Default struct {
			Model, BaseURL, Style, APIKeyEnv string
			KeySet                           bool
		} `json:"default"`
		Providers   []doctorEntry `json:"providers"`
		Classifiers []doctorEntry `json:"classifiers"`
		Skills      doctorCount   `json:"skills"`
		Mcp         doctorCount   `json:"mcp"`
		Workflows   doctorCount   `json:"workflows"`
		Models      []string      `json:"models"`
		Shell       struct {
			OS, Arch, Using, Path, Problem string
			Available                      []string
		} `json:"shell"`
		Problems []string `json:"problems"`
	}
	if err := call("GET", "/api/doctor", nil, &d); err != nil {
		return err
	}

	fmt.Printf("default model    %s\n", d.Default.Model)
	fmt.Printf("                 %s (%s)\n", d.Default.BaseURL, d.Default.Style)
	fmt.Printf("                 %s %s\n", d.Default.APIKeyEnv, tick(d.Default.KeySet, "set", "NOT SET"))
	if len(d.Models) > 1 {
		fmt.Printf("offered models   %s\n", strings.Join(d.Models, ", "))
	}

	fmt.Printf("\nplatform         %s/%s\n", d.Shell.OS, d.Shell.Arch)
	if d.Shell.Problem != "" {
		fmt.Printf("shell            ✗ %s\n", d.Shell.Problem)
	} else {
		fmt.Printf("shell            %s  (%s)\n", d.Shell.Using, d.Shell.Path)
		if len(d.Shell.Available) > 1 {
			fmt.Printf("                 also available: %s — a step may name one with `shell:`\n",
				strings.Join(d.Shell.Available[1:], ", "))
		}
	}

	fmt.Printf("\nproviders (%d)\n", len(d.Providers))
	for _, p := range d.Providers {
		printEntry(p)
	}
	fmt.Printf("\nclassifiers (%d)\n", len(d.Classifiers))
	for _, c := range d.Classifiers {
		printEntry(c)
	}

	fmt.Printf("\nskills %d   mcp servers %d   workflows %d\n", d.Skills.Count, d.Mcp.Count, d.Workflows.Count)
	// Capped: a machine with a big skills directory has dozens of duplicate
	// names, and a page of them buries the problems underneath.
	for i, sk := range d.Skills.Skipped {
		if i == 5 {
			fmt.Printf("  … and %d more skipped skills (wfx registry skills)\n", len(d.Skills.Skipped)-5)
			break
		}
		fmt.Printf("  skipped skill %s\n", sk)
	}

	if len(d.Problems) == 0 {
		fmt.Printf("\neverything named is resolvable on this machine.\n")
		return nil
	}
	fmt.Printf("\n%d problem(s) — a step naming one of these fails when it runs:\n", len(d.Problems))
	for _, p := range d.Problems {
		fmt.Printf("  ✗ %s\n", p)
	}
	return nil
}

type doctorEntry struct {
	Name, Kind, Model, Detail, Problem string
	Ready                              bool
}

type doctorCount struct {
	Count   int
	Skipped []string
}

func printEntry(e doctorEntry) {
	mark := "✓"
	if !e.Ready {
		mark = "✗"
	}
	detail := e.Detail
	if e.Model != "" {
		detail = e.Model + "  " + detail
	}
	fmt.Printf("  %s %-14s %-6s %s\n", mark, e.Name, e.Kind, detail)
	if e.Problem != "" {
		fmt.Printf("      %s\n", e.Problem)
	}
}

func tick(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

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
			fmt.Printf("\n%d skipped (a skill that does not parse is invisible — `wfx registry skills --json` for the list)\n", n)
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

// plural is for counts a person reads: "1 machine", "2 machines", "no machine".
func plural(n int, noun string) string {
	switch n {
	case 0:
		return "no " + noun
	case 1:
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
