package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tn "github.com/muthuishere/toolnexus/golang"
	"gopkg.in/yaml.v3"

	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The three tools that let AUTHORING A WORKFLOW BE A WORKFLOW.
//
// An agent asked to write one needs exactly what a person needs: what names
// exist on this machine, whether the draft is valid, and whether it would
// actually run. Given those as tools, it checks its own work inside the loop
// instead of handing over something plausible — which is the difference between
// a generator and something you can trust.
//
// None of them writes. A definition is PROPOSED as the step's typed output and
// a human saves it. An agent that could install a workflow could install one
// that runs on a schedule, and that is not a power to grant as a side effect of
// drafting.
//
// toolnexus owns the loop; these are the tools (ADR 0001). Nothing here knows
// how a model works.
func (e *Engine) platformTools(names []string) []tn.Tool {
	var out []tn.Tool
	for _, n := range names {
		switch n {
		case skills.ToolCatalog:
			out = append(out, e.catalogTool())
		case skills.ToolValidate:
			out = append(out, e.validateTool())
		case skills.ToolDryRun:
			out = append(out, e.dryRunTool())
		}
	}
	return out
}

// catalogTool answers "what may I name here?". Without it an agent invents
// plausible skill names and the whole file is refused at load.
func (e *Engine) catalogTool() tn.Tool {
	return tn.NativeTool(skills.ToolCatalog,
		"List everything a workflow may NAME on this machine: skills, built-in tools, providers, "+
			"classifiers and MCP servers. A name that is not in this list makes the entire workflow "+
			"fail to load, so check here before writing one.",
		tn.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []any{"all", "shape", "skills", "tools", "providers", "classifiers", "mcp"},
					"description": "which catalogue to list; `shape` is the structure of a workflow file itself; omit for all",
				},
				"search": map[string]any{"type": "string", "description": "only entries matching this text"},
			},
			"additionalProperties": false,
		},
		func(_ context.Context, args map[string]any) (string, error) {
			kind, _ := args["kind"].(string)
			needle, _ := args["search"].(string)
			return e.catalogText(kind, needle), nil
		})
}

func (e *Engine) catalogText(kind, needle string) string {
	match := func(s ...string) bool {
		if needle == "" {
			return true
		}
		return strings.Contains(strings.ToLower(strings.Join(s, " ")), strings.ToLower(needle))
	}
	var b strings.Builder
	want := func(k string) bool { return kind == "" || kind == "all" || kind == k }

	// The SHAPE comes first, because a name is useless without somewhere to put
	// it. An agent given only the catalogues invented `title`, `type` and
	// `input` from other workflow formats and spent twelve dry runs on it.
	if want("shape") {
		b.WriteString(workflow.DescribeShape())
		b.WriteString("\n")
	}
	if want("skills") {
		var lines []string
		for _, s := range e.skills.List() {
			if match(s.Name, s.Description) {
				lines = append(lines, fmt.Sprintf("  %-28s %s", s.Name, firstLineOf(s.Description, 110)))
			}
		}
		sort.Strings(lines)
		fmt.Fprintf(&b, "SKILLS (%d)\n%s\n\n", len(lines), strings.Join(lines, "\n"))
	}
	if want("tools") {
		var lines []string
		for _, t := range skills.Builtins() {
			if match(t.Name, t.Description) {
				lines = append(lines, fmt.Sprintf("  %-14s %s", t.Name, firstLineOf(t.Description, 110)))
			}
		}
		fmt.Fprintf(&b, "BUILT-IN TOOLS (%d)\n%s\n\n", len(lines), strings.Join(lines, "\n"))
	}
	if want("providers") {
		// The doctor's verdict, not merely the registry: a provider that cannot
		// run here is worse than one that does not exist, because it looks fine.
		var lines []string
		for _, p := range e.Doctor().Providers {
			if !match(p.Name, p.Kind, p.Model) {
				continue
			}
			state := "ready"
			if !p.Ready {
				state = "NOT USABLE: " + p.Problem
			}
			lines = append(lines, fmt.Sprintf("  %-14s %-5s %-30s %s", p.Name, p.Kind, p.Model, state))
		}
		fmt.Fprintf(&b, "PROVIDERS (%d) — a step names one in `provider:`; omit it for the default\n%s\n\n",
			len(lines), strings.Join(lines, "\n"))
	}
	if want("classifiers") {
		var lines []string
		for _, c := range e.catalog.Classifiers.List() {
			if match(c.Name, c.Backend, c.Description) {
				lines = append(lines, fmt.Sprintf("  %-14s %-11s %s", c.Name, c.Backend, firstLineOf(c.Description, 90)))
			}
		}
		fmt.Fprintf(&b, "CLASSIFIERS (%d) — a `judge` step names one in `classifier:`\n%s\n\n",
			len(lines), strings.Join(lines, "\n"))
	}
	if want("mcp") {
		var lines []string
		for _, m := range e.catalog.Mcp.List() {
			if match(m.Name, m.Description) {
				lines = append(lines, "  "+m.Name)
			}
		}
		if len(lines) == 0 {
			lines = []string{"  (none configured)"}
		}
		fmt.Fprintf(&b, "MCP SERVERS (%d)\n%s\n", len(lines), strings.Join(lines, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// validateTool runs the loader's own validation, so an agent cannot talk itself
// into believing a definition is fine.
func (e *Engine) validateTool() tn.Tool {
	return tn.NativeTool(skills.ToolValidate,
		"Validate a workflow definition against the loader's own rules. Pass the definition as JSON. "+
			"Returns either `valid` or the exact error the loader would give.",
		tn.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"definition": map[string]any{
					"type":        "object",
					"description": "the whole workflow definition",
				},
			},
			"required":             []any{"definition"},
			"additionalProperties": false,
		},
		func(_ context.Context, args map[string]any) (string, error) {
			def, err := decodeDefinition(args["definition"])
			if err != nil {
				return "", err
			}
			if bad := unknownFieldsIn(args["definition"]); len(bad) > 0 {
				return "INVALID: " + bad, nil
			}
			if err := workflow.Check(def, e.validator()); err != nil {
				return "INVALID: " + err.Error(), nil
			}
			return fmt.Sprintf("valid — %q, %d step(s)", def.Name, len(def.Steps)), nil
		})
}

// dryRunTool is the one that makes the difference: it answers whether the draft
// would actually RUN here, which validation does not.
func (e *Engine) dryRunTool() tn.Tool {
	return tn.NativeTool(skills.ToolDryRun,
		"Dry run a workflow definition: resolve every name against this machine, render every prompt, "+
			"compute the execution order and the cost ceiling. Calls no model and writes nothing. "+
			"Use this before proposing a workflow — it catches a provider whose key is unset, a prompt "+
			"referencing a field that does not exist, and a step reading another step that runs later.",
		tn.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"definition": map[string]any{"type": "object", "description": "the whole workflow definition"},
				"input":      map[string]any{"type": "object", "description": "sample input; defaults are filled in for anything omitted"},
			},
			"required":             []any{"definition"},
			"additionalProperties": false,
		},
		func(_ context.Context, args map[string]any) (string, error) {
			def, err := decodeDefinition(args["definition"])
			if err != nil {
				return "", err
			}
			// Checked here too: a guessed field name is the failure most likely
			// to send an author round in circles, and the dry run is the tool
			// they will reach for.
			if bad := unknownFieldsIn(args["definition"]); bad != "" {
				return "WOULD FAIL: " + bad, nil
			}
			input, _ := args["input"].(map[string]any)
			d := e.DryRunDraft(def, input)

			var b strings.Builder
			b.WriteString(d.Summary())
			if len(d.Waves) > 0 {
				b.WriteString("\n\norder:")
				for i, w := range d.Waves {
					fmt.Fprintf(&b, "\n  %d. %s", i+1, strings.Join(w, ", "))
				}
			}
			for _, s := range d.Steps {
				if s.Prompt != "" {
					fmt.Fprintf(&b, "\n\n--- %s would be asked ---\n%s", s.ID, trim(s.Prompt, 700))
				}
			}
			return b.String(), nil
		})
}

// decodeDefinition reads the definition in the FILE's dialect, not the API's.
//
// The two differ: a workflow file says `output_schema`, `max_turns`,
// `requires_approval`, while the JSON API says `outputSchema`, `maxTurns`,
// `requiresApproval`. The struct carries both tags, so which one applies is
// decided by the decoder.
//
// It has to be the file's. Every example an author has seen is a YAML file,
// and `wf_catalog kind=shape` describes the file — so a tool that quietly
// decoded the other dialect would drop `output_schema` on the floor and then
// report the step as having none. That is exactly what happened: an agent
// wrote a correct step four times and was told four times that it was missing
// the field it had just written.
//
// So the map is re-marshalled to YAML and decoded with the yaml tags, which is
// the same path a file takes.
func decodeDefinition(v any) (*workflow.Definition, error) {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("definition is not an object: %w", err)
	}
	var def workflow.Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("definition could not be read: %w", err)
	}
	return &def, nil
}

func firstLineOf(s string, n int) string {
	if i := strings.IndexAny(s, ".\n"); i > 20 {
		s = s[:i+1]
	}
	return trim(strings.TrimSpace(s), n)
}

// unknownFieldsIn reports step keys that are not fields, because yaml and json
// both DROP an unknown key silently — so a guessed name does not fail where it
// was written, it fails later as something that makes no sense.
func unknownFieldsIn(v any) string {
	raw, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	steps, _ := raw["steps"].([]any)
	var problems []string
	for i, st := range steps {
		m, ok := st.(map[string]any)
		if !ok {
			continue
		}
		if bad := workflow.UnknownStepFields(m); len(bad) > 0 {
			id, _ := m["id"].(string)
			if id == "" {
				id = fmt.Sprintf("step %d", i)
			}
			problems = append(problems, fmt.Sprintf("%s has no such field(s): %s", id, strings.Join(bad, ", ")))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") +
		". Call wf_catalog with kind=\"shape\" for the fields a step actually has."
}
