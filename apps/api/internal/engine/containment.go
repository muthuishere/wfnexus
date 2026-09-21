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
		// An absolute path outside the workspace needs no directory change at
		// all. Measured escapes this catches, all of which the directory-change
		// scanner missed entirely: `cat /etc/passwd`, `echo x > /tmp/pwned`,
		// `rsync -a . /tmp/exfil/`, `find / -execdir …`.
		for _, path := range absolutePaths(cmd) {
			if outside(root, path) && !systemReadable(path) {
				return fmt.Sprintf(
					"this command touches %q, which is outside the run's workspace (%s). "+
						"Work only inside the workspace, with paths relative to it.", path, workdir)
			}
		}
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

// absolutePaths returns every absolute-looking path token in a command. It is
// deliberately blunt: a token starting with `/` that is not an option.
func absolutePaths(cmd string) []string {
	var out []string
	for _, tok := range shellTokens(cmd) {
		t := strings.TrimLeft(tok, "<>|&")
		if len(t) < 1 || t[0] != '/' {
			continue
		}
		out = append(out, t)
	}
	return out
}

// systemReadable allows the read-only system locations a normal command needs —
// an interpreter, a binary, a shared library. Anything here is reachable
// without this guardrail's help anyway (`python3` resolves through PATH), so
// denying them buys nothing and breaks ordinary work.
//
// This list is the honest weakness of the whole approach: it enumerates what is
// ALLOWED OUT, which is the same escape-enumeration mistake one level down. Real
// containment enumerates what is reachable IN, which is ADR 0015's job.
func systemReadable(path string) bool {
	for _, prefix := range []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/opt/homebrew/", "/System/", "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
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
