package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/assets"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/eval"
)

// `wfx eval` — the portability matrix (ADR 0019).
//
//	wfx eval mock-demo --corpus workflows/mock-demo.eval.yaml --providers mock,ollama-http
//
// IT RUNS IN THIS PROCESS, not through the server API — the one verb besides
// `wfx judge` that does, and for the same reason. Every other verb (`run`,
// `dryrun`) is a thin client because it acts on the platform's shared state.
// An eval produces no state anybody needs: it is dozens of sample runs whose
// only product is the table, and posting them to a server would bury the runs
// people actually look at. More importantly, ADR 0019's enterprise case is "a
// passing row for THEIR model, produced on THEIR hardware": in-process means
// the backends are the ones this machine resolves — its registries, its CLIs,
// its keys — and no account or running server is needed (the one-person
// scale). The engine is the real one, so a cell means what a real run means.
//
// Evals cost real money on a paid provider; `wfx dryrun` is the free,
// structural check. They are complementary, not alternatives.

const evalUsage = `wfx eval <workflow> --corpus <file.yaml> --providers a,b,c [--json] [--timeout 30m]`

func cmdEval(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: %s", evalUsage)
	}
	wf := args[0]
	corpusPath := flagOf(args, "--corpus", "")
	providers := splitList(flagOf(args, "--providers", ""))
	if corpusPath == "" || len(providers) == 0 {
		return fmt.Errorf("usage: %s", evalUsage)
	}
	timeout, err := time.ParseDuration(flagOf(args, "--timeout", "30m"))
	if err != nil {
		return fmt.Errorf("--timeout: %w", err)
	}
	c, err := eval.LoadCorpus(corpusPath)
	if err != nil {
		return err
	}
	if c.Workflow != wf {
		return fmt.Errorf("the corpus is for %q, not %q", c.Workflow, wf)
	}

	cfg, err := config.LoadWithFile(config.DefaultPath())
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := resolveEvalAssets(&cfg); err != nil {
		return err
	}
	// The engine logs every step to the standard logger; the matrix is the
	// output of this command, so the chatter goes nowhere unless asked for.
	if !has(args, "-v") {
		log.SetOutput(io.Discard)
	}
	loc, err := eval.NewLocal(cfg, timeout)
	if err != nil {
		return err
	}
	defer loc.Close()

	m := eval.Run(context.Background(), loc, c, providers)
	if has(args, "--json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			return err
		}
	} else {
		printMatrix(os.Stdout, m)
	}
	// A failing cell fails the command, so a script or CI step can gate on it.
	failed, cells := 0, 0
	for _, r := range m.Rows {
		for _, cell := range r.Cells {
			cells++
			if !cell.Pass {
				failed++
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d cells failed", failed, cells)
	}
	return nil
}

// resolveEvalAssets finds registries, skills and templates the way the server
// does: disk first, the binary's embedded copy as the fallback.
func resolveEvalAssets(cfg *config.Config) error {
	for _, it := range []struct {
		name     string
		target   *string
		explicit bool
	}{
		{assets.TemplatesName, &cfg.TemplatesDir, cfg.Explicit.Templates},
		{assets.SkillsName, &cfg.SkillsDir, cfg.Explicit.Skills},
		{assets.RegistriesName, &cfg.RegistriesPath, cfg.Explicit.Registries},
	} {
		path, origin, _, err := assets.Resolve(it.name, *it.target, it.explicit)
		if err != nil {
			return fmt.Errorf("%s: %w", it.name, err)
		}
		if origin != assets.Missing {
			*it.target = path
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// printMatrix renders rows = cases, columns = providers, then the totals, then
// why each failing cell failed — a red cell that does not say why sends
// somebody to the run log, and the eval's runs are thrown away.
func printMatrix(w io.Writer, m *eval.Matrix) {
	width := 14
	for _, p := range m.Providers {
		if len(p)+2 > width {
			width = len(p) + 2
		}
	}
	caseW := 10
	for _, r := range m.Rows {
		if len(r.Case)+2 > caseW {
			caseW = len(r.Case) + 2
		}
	}
	fmt.Fprintf(w, "%s — %d cases × %d providers\n\n", m.Workflow, len(m.Rows), len(m.Providers))
	fmt.Fprintf(w, "%-*s", caseW, "CASE")
	for _, p := range m.Providers {
		fmt.Fprintf(w, "%-*s", width, p)
	}
	fmt.Fprintln(w)
	for _, r := range m.Rows {
		fmt.Fprintf(w, "%-*s", caseW, r.Case)
		for _, c := range r.Cells {
			fmt.Fprintf(w, "%-*s", width, cellText(c))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	line := func(label string, f func(eval.Total) string) {
		fmt.Fprintf(w, "%-*s", caseW, label)
		for _, t := range m.Totals {
			fmt.Fprintf(w, "%-*s", width, f(t))
		}
		fmt.Fprintln(w)
	}
	line("pass rate", func(t eval.Total) string {
		return fmt.Sprintf("%d/%d %.0f%%", t.Passed, t.Cases, t.PassRate*100)
	})
	line("turns", func(t eval.Total) string { return fmt.Sprint(t.Turns) })
	line("tokens", func(t eval.Total) string {
		return fmt.Sprintf("%s/%s", compact(t.PromptTokens), compact(t.CompletionTokens))
	})
	line("cost", func(t eval.Total) string {
		if !t.CostKnown {
			return "?"
		}
		return fmt.Sprintf("$%.2f", t.CostUsd)
	})
	line("wall", func(t eval.Total) string {
		return (time.Duration(t.WallMs) * time.Millisecond).Round(100 * time.Millisecond).String()
	})
	fmt.Fprintln(w, "\ntokens are prompt/completion; cost ? = a price nobody set (wfx prices)")

	var why []string
	for _, r := range m.Rows {
		for _, c := range r.Cells {
			if c.Pass {
				continue
			}
			head := fmt.Sprintf("  %s on %s: status %s", r.Case, c.Provider, c.Status)
			if c.Error != "" {
				head += " — " + firstLine(c.Error)
			}
			why = append(why, head)
			for _, res := range c.Results {
				if res.Pass {
					continue
				}
				a := res.Assertion
				s := fmt.Sprintf("    %s.%s %s %v", a.Step, or(a.Path, "(output)"), a.Op, jsonText(a.Value))
				if res.Detail != "" {
					s += " — " + res.Detail
				} else {
					s += " — got " + jsonText(res.Actual)
				}
				why = append(why, s)
			}
		}
	}
	if len(why) > 0 {
		fmt.Fprintln(w, "\nfailures:")
		fmt.Fprintln(w, strings.Join(why, "\n"))
	}
}

func cellText(c eval.Cell) string {
	if c.Pass {
		return "pass"
	}
	failed := 0
	for _, r := range c.Results {
		if !r.Pass {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Sprintf("FAIL %d/%d", failed, len(c.Results))
	}
	return "FAIL " + c.Status
}

func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func jsonText(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
