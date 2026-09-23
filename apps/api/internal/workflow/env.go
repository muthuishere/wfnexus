package workflow

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ENVIRONMENT FOR A STEP
//
// A skill often needs something from the environment — a token for the API it
// talks to, a registry URL, a feature flag. `env:` cascades workflow → job →
// step, the same direction as `runs-on:` and `defaults:`.
//
//	env:
//	  NODE_ENV: test              # a literal: it is committed, so it is not a secret
//	  GH_TOKEN: ${GITHUB_PAT}     # a REFERENCE: read where the step runs
//
// The distinction is the whole design. A literal is fine in a file anyone can
// read. A credential is not, so it is named and never written: `${GITHUB_PAT}`
// means "whatever GITHUB_PAT holds on the machine running this step", which is
// the same rule `apiKeyEnv` already follows for provider keys.
//
// That also makes the workflow portable and the worker safe: what crosses the
// wire to another machine is the reference, and the value is read from that
// machine's own environment. A secret belonging to a build box never has to be
// known by the platform to be used there.

// envRef matches ${NAME} and $NAME.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// EnvKeys returns the variable names an env block reads from the environment.
// Names, never values — this is what a dry run reports and what the UI shows.
func EnvKeys(env map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range env {
		for _, m := range envRef.FindAllStringSubmatch(v, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ResolveEnv expands an env block against the environment of the machine that
// is about to run the step, returning "K=V" pairs for exec.Cmd.
//
// A reference to a variable that is not set is an ERROR naming the variable.
// The alternative — an empty string — is how a step runs with no credential and
// fails somewhere far away with a 401 that says nothing about the cause.
func ResolveEnv(env map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	var missing []string
	out := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		var unset []string
		v := envRef.ReplaceAllStringFunc(env[k], func(ref string) string {
			m := envRef.FindStringSubmatch(ref)
			name := m[1]
			if name == "" {
				name = m[2]
			}
			val, ok := os.LookupEnv(name)
			if !ok {
				unset = append(unset, name)
			}
			return val
		})
		missing = append(missing, unset...)
		out = append(out, k+"="+v)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("env: %s is not set on this machine", strings.Join(dedupe(missing), ", "))
	}
	return out, nil
}

// ShellPrefix renders an env block as assignments to put in front of a command
// the AGENT runs, for the builtin `bash` tool — which has no env argument and
// inherits this process's environment instead.
//
// A reference stays a reference: `GH_TOKEN="$GITHUB_PAT"` is expanded by the
// child shell out of the environment it already inherited. So the value is
// never rendered, never logged, and never appears in the tool call the run
// records — only the name does, which is the same thing the file says.
func ShellPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		v := env[k]
		if envRef.MatchString(v) {
			// Double quotes so the shell expands it; the reference is
			// normalised to ${NAME} so adjacent text cannot swallow the name.
			b.WriteString(k + `="` + envRef.ReplaceAllStringFunc(v, normalizeRef) + `" `)
			continue
		}
		b.WriteString(k + "=" + shellQuote(v) + " ")
	}
	return b.String()
}

func normalizeRef(ref string) string {
	m := envRef.FindStringSubmatch(ref)
	name := m[1]
	if name == "" {
		name = m[2]
	}
	return "${" + name + "}"
}

// shellQuote makes a literal safe inside single quotes, the only quoting a
// POSIX shell does not reinterpret.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// MergeEnv layers child over parent without touching either.
func MergeEnv(parent, child map[string]string) map[string]string {
	if len(parent) == 0 && len(child) == 0 {
		return nil
	}
	out := make(map[string]string, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// secretish is a name that almost certainly holds a credential.
var secretish = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[-_]?key|private[-_]?key|credential)`)

// CheckEnv refuses a credential written as a literal.
//
// The file is committed and read by everyone who can see the repository, so a
// value under a name like GITHUB_TOKEN is a leak the moment it is saved — and
// it is a leak that looks like ordinary configuration, which is why it has to
// be refused rather than warned about.
func CheckEnv(where string, env map[string]string) error {
	for _, k := range sortedKeys(env) {
		v := env[k]
		if v == "" || envRef.MatchString(v) || !secretish.MatchString(k) {
			continue
		}
		return fmt.Errorf("%s: env %s looks like a credential written into the file. "+
			"Name the variable instead — %s: ${%s} — and the value is read where the step runs, "+
			"never committed and never sent to another machine", where, k, k, k)
	}
	return nil
}
