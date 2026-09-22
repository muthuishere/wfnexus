// Package shell resolves the interpreter a `run:` step executes through.
//
// The platform matrix is Linux, WSL, macOS and Windows, and "the shell" is not
// the same program on all four. Hardcoding one is how a workflow that works on
// a developer's Mac fails on a Windows machine with an error about a missing
// executable rather than about the command.
//
// GitHub Actions solved this by naming the shell in the workflow file and
// documenting the per-runner default, and that is the shape copied here: a step
// may say `shell: bash`, and the default is whatever this machine actually has.
package shell

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Shell is a resolved interpreter: the program and the flags that make it read
// a command from an argument.
type Shell struct {
	// Name is the shell as a workflow would name it: bash, sh, pwsh, cmd.
	Name string
	// Path is the resolved executable. Empty means this shell is unavailable.
	Path string
	// Flags precede the command string in argv.
	Flags []string
}

// Available reports whether this shell can actually be run.
func (s Shell) Available() bool { return s.Path != "" }

// Command builds the argv for a command string.
func (s Shell) Command(cmd string) []string {
	return append(append([]string{s.Path}, s.Flags...), cmd)
}

func (s Shell) String() string {
	if !s.Available() {
		return s.Name + " (not found)"
	}
	return s.Name + " " + strings.Join(s.Flags, " ") + "  → " + s.Path
}

// known is every shell name a workflow may use, with the flags that make it
// read a command from an argument. It is deliberately NOT platform-dependent:
// `shell: pwsh` is a valid thing for a workflow to say, and whether it RUNS is
// a property of the machine, not of the spelling. Making the vocabulary
// per-platform meant `wfx validate` rejected a portable workflow as "unknown
// shell" on Linux while accepting it on Windows — the error blamed the author
// for the validator's location.
//
// `bash -c`, not `bash -lc`: a login shell sources the user's profile, so the
// command sees a different PATH, different aliases and a different environment
// depending on whose machine it is. A workflow step must not depend on a
// developer's dotfiles — that is the whole reason it is written down.
var known = []Shell{
	{Name: "bash", Flags: []string{"-c"}},
	{Name: "sh", Flags: []string{"-c"}},
	{Name: "pwsh", Flags: []string{"-NoProfile", "-NonInteractive", "-Command"}},
	{Name: "powershell", Flags: []string{"-NoProfile", "-NonInteractive", "-Command"}},
	{Name: "cmd", Flags: []string{"/d", "/s", "/c"}},
}

// candidates is the preference order for a step that names no shell.
//
// POSIX first on every platform, Windows included: Git for Windows and WSL both
// provide a real POSIX shell, so a workflow written once keeps working. Falling
// back to PowerShell would silently change the language the command is written
// in, which is worse than not running.
func candidates() []Shell {
	order := []string{"bash", "sh"}
	if runtime.GOOS == "windows" {
		order = append(order, "pwsh", "powershell", "cmd")
	}
	out := make([]Shell, 0, len(order))
	for _, name := range order {
		for _, s := range known {
			if s.Name == name {
				out = append(out, s)
			}
		}
	}
	return out
}

// Lookup resolves one shell by name. An empty name means the platform default.
func Lookup(name string) (Shell, error) {
	list := candidates()
	if name == "" {
		for _, s := range list {
			if path, err := exec.LookPath(s.Name); err == nil {
				s.Path = path
				return s, nil
			}
		}
		return Shell{}, fmt.Errorf("no shell found on PATH (tried %s) — a `run:` step needs one; "+
			"on Windows install Git for Windows or enable WSL", strings.Join(names(list), ", "))
	}
	for _, s := range known {
		if s.Name != name {
			continue
		}
		path, err := exec.LookPath(s.Name)
		if err != nil {
			// Named, recognised, not installed. Reported separately from an
			// unknown name because they are different problems and only one of
			// them is the workflow author's to fix.
			return Shell{Name: name, Flags: s.Flags},
				fmt.Errorf("shell %q is not on PATH on this machine (%s)", name, runtime.GOOS)
		}
		s.Path = path
		return s, nil
	}
	return Shell{Name: name}, fmt.Errorf("unknown shell %q — supported: %s", name, strings.Join(names(known), ", "))
}

// Default is the shell a step gets when it names none. WFX_SHELL overrides the
// preference order for a machine whose operator knows better.
func Default() (Shell, error) { return Lookup(os.Getenv("WFX_SHELL")) }

// Known is every shell name a workflow may name, for validation and for the
// builder's dropdown.
func Known() []string { return names(known) }

// Report lists every candidate for THIS platform and whether it is present,
// for `wfx doctor`.
func Report() []Shell {
	out := candidates()
	for i := range out {
		if path, err := exec.LookPath(out[i].Name); err == nil {
			out[i].Path = path
		}
	}
	return out
}

func names(list []Shell) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}
