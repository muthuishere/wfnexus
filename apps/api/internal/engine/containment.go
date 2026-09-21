package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

// Pinning bash to a workdir is ADVISORY, not containment. The workdir parameter
// sets only the INITIAL directory; `cd /elsewhere && …` inside the command
// string leaves it, and the tool still reports success.
//
// Observed for real: a draft-pr agent ran
//
//	cd /Users/…/wfnexus && git log --all --grep=reorder
//
// and then `git checkout -b`, `git stash` and `git reset` — in the PLATFORM'S
// OWN repository rather than the run's worktree. It knew that path because the
// skills it loads live there, so their absolute paths were already in its
// context.
//
// Every step and every sub-agent therefore gets this guardrail, ahead of the
// rules its YAML declares and regardless of what that YAML says. Scoping is the
// security model, and a shell that can leave its workspace is not scoped.

// containmentGuardrail denies any shell command that steps outside workdir.
// An empty workdir disables it — there is nothing to contain.
func containmentGuardrail(workdir string) agents.Guardrail {
	// Resolve ONCE and use the resolved form as both the comparison root and the
	// base for relative targets. Resolving only the root while joining against
	// the raw path made every relative `cd` look like an escape on macOS, where
	// /var is a symlink to /private/var.
	root := workdir
	if resolved, err := filepath.EvalSymlinks(workdir); err == nil {
		root = resolved
	}
	return func(ev tn.BeforeToolEvent) string {
		if workdir == "" || ev.Name != "bash" {
			return ""
		}
		cmd, _ := ev.Args["command"].(string)
		for _, target := range escapeTargets(cmd) {
			if outside(root, target) {
				return fmt.Sprintf(
					"this command leaves the run's workspace (%q is outside %s). "+
						"Everything you need is inside the workspace: use paths relative to it and do not cd elsewhere. "+
						"The platform's own files are not yours to read or change.",
					target, workdir)
			}
		}
		return ""
	}
}

// escapeTargets pulls every directory a command would move to: `cd <path>`,
// `pushd <path>`, and git's own repo overrides (-C, --git-dir, --work-tree).
// Go's regexp has no backreferences, so the command is tokenised rather than
// pattern-matched — which also handles quoting honestly.
func escapeTargets(cmd string) []string {
	var out []string
	toks := shellTokens(cmd)
	at := func(i int) string {
		if i < len(toks) {
			return toks[i]
		}
		return ""
	}
	for i, tok := range toks {
		switch {
		case tok == "cd" || tok == "pushd":
			if t := at(i + 1); t != "" {
				out = append(out, t)
			} else {
				out = append(out, "~") // bare `cd` is $HOME
			}
		case tok == "-C", tok == "--git-dir", tok == "--work-tree":
			if t := at(i + 1); t != "" {
				out = append(out, t)
			}
		case strings.HasPrefix(tok, "--git-dir="):
			out = append(out, strings.TrimPrefix(tok, "--git-dir="))
		case strings.HasPrefix(tok, "--work-tree="):
			out = append(out, strings.TrimPrefix(tok, "--work-tree="))
		}
	}
	return out
}

// shellTokens splits on whitespace and the separators that begin a new command,
// stripping surrounding quotes. Deliberately crude: anything it cannot read
// confidently becomes a token the caller refuses.
func shellTokens(cmd string) []string {
	repl := strings.NewReplacer(
		";", " ", "&&", " ", "||", " ", "|", " ", "(", " ", ")", " ", "\n", " ", "\t", " ",
	)
	fields := strings.Fields(repl.Replace(cmd))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.Trim(f, `"'`))
	}
	return out
}

// outside reports whether target escapes the workspace. Relative targets resolve
// against it; `-`, `~` and unexpanded variables are refused because none of them
// can be shown to stay inside.
func outside(root, target string) bool {
	switch {
	case target == "-", target == "~", strings.HasPrefix(target, "~/"):
		return true
	case strings.HasPrefix(target, "$"):
		return true
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, target)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
