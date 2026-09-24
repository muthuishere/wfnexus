package engine

import (
	"fmt"
	"path/filepath"
	"runtime"
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
//
// mounts are the WRITABLE folders this run attached (`mount:` with `:rw`). They
// widen containment by exactly what the workflow FILE declared and nothing
// else — a folder a human wrote down and a reviewer saw, never a path the
// agent chose or the run's input supplied. A read-only mount is not here
// because it is a copy INSIDE the workspace, so it needs no widening at all.
func containmentGuardrail(workdir string, mounts ...string) agents.Guardrail {
	// Resolve ONCE and use the resolved form as both the comparison root and the
	// base for relative targets. Resolving only the root while joining against
	// the raw path made every relative `cd` look like an escape on macOS, where
	// /var is a symlink to /private/var.
	root := workdir
	if resolved, err := filepath.EvalSymlinks(workdir); err == nil {
		root = resolved
	}
	// A target is inside if it is inside ANY of these. The workspace is first
	// so the common case is one comparison.
	roots := append([]string{root}, mounts...)
	outside := func(_ string, target string) bool {
		for _, r := range roots {
			if !outsideRoot(r, target) {
				return false
			}
		}
		return true
	}
	deny := func(name, path string) string {
		return fmt.Sprintf(
			"%s was asked to touch %q, which is outside the run's workspace (%s). "+
				"Work only inside the workspace, with paths relative to it.", name, path, workdir)
	}
	return func(ev tn.BeforeToolEvent) string {
		if workdir == "" {
			return ""
		}
		if ev.Name != "bash" {
			// Every OTHER tool is checked too. It used to be bash alone, and a
			// relative `write` path — resolved by the builtin against the server
			// process's cwd, not the worktree — put a file straight into the
			// platform's own repository on a live run. See paths.go.
			//
			// A relative path is resolved against the workspace here, which is
			// what pinPaths makes true before the tool runs; checking it the
			// same way means this guardrail gives the same answer whether or not
			// the pinning hook ran first.
			for _, k := range pathArgs {
				if p, _ := ev.Args[k].(string); p != "" && outside(root, resolveIn(root, p)) {
					return deny(ev.Name, p)
				}
			}
			if txt, _ := ev.Args["patchText"].(string); txt != "" {
				for _, p := range patchPaths(txt) {
					if outside(root, resolveIn(root, p)) {
						return deny(ev.Name, p)
					}
				}
			}
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

// absolutePaths returns every absolute-looking path token in a command.
//
// "Absolute" is not one thing across the platforms this has to run on, and
// missing a form means the guardrail silently passes it:
//
//	/etc/passwd          POSIX — Linux, WSL, macOS
//	C:\Windows\win.ini   Windows, drive-letter
//	C:/Windows/win.ini   Windows, forward slashes (accepted by every API)
//	\\server\share       Windows, UNC
//	/c/Users/me          Git Bash on Windows, POSIX-looking and NOT the same
//	                     root Go would compute — see the note below
//
// The Git Bash form is why isWindowsAbs is checked everywhere rather than only
// under `runtime.GOOS == "windows"`: the shell's idea of a path and Go's differ
// on that platform, so both spellings are treated as absolute and are compared
// against the workspace with the platform's own rules.
func absolutePaths(cmd string) []string {
	var out []string
	for _, tok := range shellTokens(cmd) {
		t := strings.TrimLeft(tok, "<>|&")
		if t == "" {
			continue
		}
		if t[0] == '/' || isWindowsAbs(t) {
			out = append(out, t)
		}
	}
	return out
}

// isWindowsAbs reports a Windows-absolute path in any spelling: a drive letter
// with either slash, or a UNC share. filepath.IsAbs cannot be used because it
// answers for the RUNNING platform, and a token here may have been written for
// a different one.
func isWindowsAbs(p string) bool {
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") && len(p) > 2 && p[2] != '/' {
		return true // UNC
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return false
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
	// POSIX locations are checked on every platform, not only on Unix: under
	// WSL and Git Bash a command written for a POSIX shell runs on Windows and
	// still says /usr/bin.
	for _, prefix := range []string{
		"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/opt/homebrew/", "/opt/", "/System/", "/Library/",
		"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/zero", "/dev/urandom",
		"/proc/", "/etc/ssl/", "/etc/alternatives/", "/nix/store/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Windows equivalents: the interpreter, the runtime and the shared
	// libraries a normal command reaches for. Compared case-insensitively
	// because the filesystem is.
	// Backslashes are replaced explicitly: filepath.ToSlash is a no-op off
	// Windows, so a Windows-spelled path arriving on a Linux host (a workflow
	// authored elsewhere, a WSL boundary) would keep its separators and miss
	// every prefix below.
	lower := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	for _, prefix := range []string{
		"c:/windows/", "c:/program files/", "c:/program files (x86)/", "c:/programdata/chocolatey/",
		// Git Bash lays a POSIX tree over the drive, so its interpreters show
		// up as /c/program files/... or /mingw64/...
		"/c/windows/", "/c/program files/", "/mingw64/", "/mingw32/", "/usr/bin/",
	} {
		if strings.HasPrefix(lower, prefix) {
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

// outside reports whether target escapes a single root — the package-level
// form, for callers with exactly one.
func outside(root, target string) bool { return outsideRoot(root, target) }

// outsideRoot reports whether target escapes root. Relative targets resolve
// against it; `-`, `~` and unexpanded variables are refused because none of them
// can be shown to stay inside.
func outsideRoot(root, target string) bool {
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
	abs = resolveExisting(filepath.Clean(abs))
	rel, err := filepath.Rel(caseFold(root), caseFold(abs))
	if err != nil {
		// Different drives on Windows, or anything else Rel cannot relate. A
		// path we cannot place is treated as outside: this fails CLOSED, which
		// is the rule ADR 0006 keeps having to relearn.
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveIn resolves a tool's path argument the way the workspace requires:
// relative to the workspace root, never to whatever directory this process
// happens to be running in.
func resolveIn(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

// resolveExisting resolves symlinks as far down the path as actually exists,
// then re-attaches the rest.
//
// filepath.EvalSymlinks fails outright on a path that does not exist yet, and
// a `write` names a file that by definition does not. Leaving such a path
// unresolved compares an unresolved target against a resolved root, and on
// macOS — where /var is a symlink to /private/var — every write to a new file
// under a temp workspace then reads as an escape. That is the same
// resolved-root-versus-unresolved-path bug that once denied every relative
// `cd`, met a second time from the other side.
func resolveExisting(abs string) string {
	rest := ""
	for cur := abs; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs // nothing along the path exists; compare it as written
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// caseFold normalises a path for comparison on platforms whose filesystem is
// case-insensitive. Without it, a workspace at C:\\Work and a tool argument
// spelled c:\\work\\main.go compare as unrelated and the write reads as an
// escape — the Windows version of the macOS /var symlink bug.
//
// macOS is also case-insensitive by default, but its paths round-trip through
// EvalSymlinks with their real case, so folding there would hide nothing and
// risks conflating two genuinely distinct paths on a case-sensitive volume.
func caseFold(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
