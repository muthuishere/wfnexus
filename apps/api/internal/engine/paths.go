package engine

import (
	"path/filepath"
	"regexp"
	"strings"
)

// The file builtins resolve a relative path with plain os.ReadFile /
// os.WriteFile, so it lands against the SERVER PROCESS'S working directory —
// not the run's worktree. Nothing in the tool call says otherwise and nothing
// in the result does either.
//
// Observed for real on 2026-09-22: a live `test-backfill` run with
// `isolate: true` wrote an 18 KB `planner_test.go` into the platform's own
// checkout, having been asked to write tests into its worktree. The worktree
// got a 50-byte stub. Both the agent and the run reported success.
//
// The containment guardrail did not see it, because the guardrail only
// inspected `bash` — it enumerated the escapes it knew (cd, absolute paths)
// and a relative path to `write` was not among them. This is the third time
// that shape has cost us something, and it is the argument for ADR 0015/0016:
// with a runner that is PID 1 with cwd = the worktree, a relative path lands
// inside by construction and none of this code is needed.
//
// Until then, relative paths are MADE workspace-relative before the tool runs.

// pathArgs are the argument names the builtins resolve as filesystem paths.
var pathArgs = []string{"path"}

// pinPaths rewrites a tool call's relative path arguments to sit inside
// workdir. It returns nil when nothing needed changing.
//
// Absolute paths are left exactly as they are: rewriting one would silently
// redirect a call the agent meant literally. They are the containment
// guardrail's business, which denies the ones outside the workspace.
func pinPaths(name string, args map[string]any, workdir string) map[string]any {
	if workdir == "" || len(args) == 0 {
		return nil
	}
	var out map[string]any
	set := func(k string, v any) {
		if out == nil {
			out = map[string]any{}
			for ak, av := range args {
				out[ak] = av
			}
		}
		out[k] = v
	}
	for _, k := range pathArgs {
		p, _ := args[k].(string)
		if p == "" || filepath.IsAbs(p) {
			continue
		}
		set(k, filepath.Join(workdir, p))
	}
	if name == "apply_patch" {
		if txt, _ := args["patchText"].(string); txt != "" {
			if pinned := pinPatchPaths(txt, workdir); pinned != txt {
				set("patchText", pinned)
			}
		}
	}
	return out
}

// patchFileMarker is toolnexus's own Begin/End Patch file marker. apply_patch
// carries its paths INSIDE the patch text, so they cannot be pinned by
// rewriting an argument — the grammar has to be read.
var patchFileMarker = regexp.MustCompile(`^\*\*\* (Add|Update|Delete) File: (.+)$`)

func pinPatchPaths(patch, workdir string) string {
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		m := patchFileMarker.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		p := strings.TrimSpace(m[2])
		if p == "" || filepath.IsAbs(p) {
			continue
		}
		lines[i] = "*** " + m[1] + " File: " + filepath.Join(workdir, p)
	}
	return strings.Join(lines, "\n")
}

// patchPaths lists every file a patch would touch, for the guardrail.
func patchPaths(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		if m := patchFileMarker.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			if p := strings.TrimSpace(m[2]); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
