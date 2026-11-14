package workflow

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

// RUN TEXT AND DATA.
//
// A `run:` step is a script. The values a template fills into it — the run's
// input, earlier steps' outputs, state, memory, loop items — are data, and
// they reach the script as data: each `{{ … }}` that prints a value becomes a
// reference to an environment variable (WFX_DATA_1, WFX_DATA_2, …) and the
// value travels in the environment beside the command. A value therefore
// arrives unchanged, whatever quotes, newlines or shell syntax it holds (a
// value that goes by file and is read back with $(cat …) loses trailing
// newlines and any NUL byte; the file itself is exact),
// and the script's text is only what the workflow author wrote.
//
// Only plain references are allowed in run text (see parseRunText); a
// template's logic belongs in the script.
//
// Prompts, gate messages and every other non-shell text keep full templates
// (Render).

const (
	// dataInline is the largest value passed as an environment variable;
	// above it, or once the step's values pass dataBudget together, the value
	// goes to a 0600 file and WFX_DATA_<n>_FILE names it.
	dataInline = 64 << 10
	dataBudget = 256 << 10
	// dataMark brackets a value's position in rendered run text until the
	// machine that runs it decides how its shell refers to a variable.
	dataMark = "\uE000wfxdata:"
	dataEnd  = "\uE001"
)

// RunText is a rendered `run:` step: the script with a mark where each value
// goes, and the values in order.
type RunText struct {
	Script string   `json:"script"`
	Data   []string `json:"data,omitempty"`
}

// What `run:` text may contain: plain references to values and nothing else.
// A reference is one action holding one field path on the root dot —
// {{ .Input.name }}, {{ .Steps.a.output.y }}, {{ .Item.z }}, {{ .RunID }},
// {{ .WorkDir }}, a state key — with optional trim markers ({{- … -}}).
// Comments are dropped. Every other construct — if, else, range, with,
// define, template, block, break, continue, variables, pipes, function calls,
// parentheses, literals — is refused at load: the logic belongs in the script,
// working on the value the script receives in its environment.
//
// Every reference, the run's own facts included, becomes a reference to an
// environment variable and its value travels beside the command. A string
// value arrives as it is; any other value (an object, a list, a number, a
// boolean) arrives as JSON, so a script parses it with jq or its own language.
//
// Prompts, gate messages and every other non-shell text keep full templates
// (Render).

// runPart is one piece of run text: literal text, or a reference (path).
type runPart struct {
	text string
	path string
}

// refPath is a field path on the root dot. Keys of steps and of the state
// scopes may hold a hyphen (and a state key a dot), as elsewhere.
var refPath = regexp.MustCompile(`^\.[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_-]+)*$`)

// rootFields are the names a reference may start with: TemplateData's fields.
var rootFields = func() map[string]bool {
	m := map[string]bool{}
	t := reflect.TypeOf(TemplateData{})
	for i := 0; i < t.NumField(); i++ {
		m[t.Field(i).Name] = true
	}
	m["Inputs"] = true // a task's or module's own inputs, substituted at load (inputs.go)
	return m
}()

// hyphenRoots are the roots whose keys may hold a hyphen (step ids, state keys).
var hyphenRoots = map[string]bool{"Steps": true, "Step": true, "Workflow": true, "Project": true, "Global": true}

const runTextAdvice = "run: text may only reference values, like {{ .Input.x }}; do the logic in the script using the env value"

// parseRunText splits run text into literal text and references, refusing
// every construct that is not a plain reference.
func parseRunText(text string) ([]runPart, error) {
	var parts []runPart
	trimNext := false
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			if trimNext {
				text = strings.TrimLeft(text, " \t\r\n")
			}
			parts = append(parts, runPart{text: text})
			return parts, nil
		}
		lit := text[:i]
		if trimNext {
			lit = strings.TrimLeft(lit, " \t\r\n")
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			return nil, fmt.Errorf("an unclosed {{ in run text")
		}
		inner := text[i+2 : i+j]
		text = text[i+j+2:]
		if strings.HasPrefix(inner, "- ") || inner == "-" || strings.HasPrefix(inner, "-\t") || strings.HasPrefix(inner, "-\n") {
			lit = strings.TrimRight(lit, " \t\r\n")
			inner = inner[1:]
		}
		trimNext = false
		if strings.HasSuffix(inner, " -") || strings.HasSuffix(inner, "\t-") || strings.HasSuffix(inner, "\n-") {
			trimNext = true
			inner = inner[:len(inner)-1]
		}
		parts = append(parts, runPart{text: lit})
		inner = strings.TrimSpace(inner)
		if strings.HasPrefix(inner, "/*") && strings.HasSuffix(inner, "*/") {
			continue // a comment
		}
		if err := checkRef(inner); err != nil {
			return nil, err
		}
		parts = append(parts, runPart{path: inner})
	}
}

// checkRef refuses anything but a plain reference, naming what it found.
func checkRef(inner string) error {
	if refPath.MatchString(inner) {
		segs := strings.Split(inner[1:], ".")
		if !rootFields[segs[0]] {
			return fmt.Errorf("{{ %s }}: .%s is not a value a step can see; %s", inner, segs[0], runTextAdvice)
		}
		if !hyphenRoots[segs[0]] {
			for _, s := range segs[1:] {
				if strings.Contains(s, "-") {
					return fmt.Errorf("{{ %s }}: the key %q can only be reached with index; reference the whole value instead (it arrives as JSON) and read the key in the script, for example with jq", inner, s)
				}
			}
		}
		return nil
	}
	word := strings.Fields(inner)
	first := ""
	if len(word) > 0 {
		first = word[0]
	}
	var what string
	switch {
	case inner == "":
		what = "an empty action"
	case map[string]bool{"if": true, "else": true, "end": true, "range": true, "with": true, "define": true,
		"template": true, "block": true, "break": true, "continue": true}[first]:
		what = "`" + first + "`"
	case strings.Contains(inner, ":=") || strings.Contains(inner, "="):
		what = "a variable assignment"
	case strings.Contains(inner, "$"):
		what = "a variable"
	case strings.Contains(inner, "|"):
		what = "a pipe"
	case strings.Contains(inner, "("):
		what = "parentheses"
	case first == "index":
		what = "index (" + inner + "); reference the whole value instead (it arrives as JSON) and read the key in the script, for example with jq —"
	case strings.HasPrefix(inner, "."):
		what = "a key that only index can reach (" + inner + "); reference the whole value instead (it arrives as JSON) and read the key in the script, for example with jq —"
	case strings.ContainsAny(first[:1], `"'`+"`0123456789-") || first == "true" || first == "false" || first == "nil":
		what = "a literal"
	default:
		what = "the function `" + first + "`"
	}
	return fmt.Errorf("{{ %s }}: %s is not allowed; %s", inner, what, runTextAdvice)
}

// refValue is the value a reference names, as the script receives it: a
// string as it is, anything else as JSON, nothing as "".
func refValue(path string, data TemplateData) (string, error) {
	var got any
	t, err := template.New("ref").Funcs(funcs).Funcs(template.FuncMap{"wfxRef": func(v any) string { got = v; return "" }}).
		Option("missingkey=zero").Parse(rewriteStatePaths(rewriteStepPaths("{{ " + path + " }}")))
	if err != nil {
		return "", err
	}
	if t.Tree != nil && len(t.Tree.Root.Nodes) == 1 {
		if a, ok := t.Tree.Root.Nodes[0].(*parse.ActionNode); ok {
			id := parse.NewIdentifier("wfxRef").SetTree(t.Tree).SetPos(a.Pos)
			a.Pipe.Cmds = append(a.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Pos: a.Pos, Args: []parse.Node{id}})
		}
	}
	if err := t.Execute(io.Discard, data); err != nil {
		return "", err
	}
	switch v := got.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	// The same JSON the `json` template function writes, so `{{ json .X }}`
	// and `{{ .X }}` give a script the same bytes for an object or a list.
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		return fmt.Sprint(got), nil
	}
	return string(b), nil
}

// RenderRun renders a `run:` step's text with every reference replaced by a
// mark (see RunText.For) and its value kept beside it.
func RenderRun(text string, data TemplateData) (RunText, error) {
	parts, err := parseRunText(text)
	if err != nil {
		return RunText{}, err
	}
	var b strings.Builder
	var vals []string
	for _, p := range parts {
		if p.path == "" {
			b.WriteString(p.text)
			continue
		}
		v, err := refValue(p.path, data)
		if err != nil {
			return RunText{}, err
		}
		vals = append(vals, v)
		b.WriteString(dataMark + strconv.Itoa(len(vals)) + dataEnd)
	}
	return RunText{Script: b.String(), Data: vals}, nil
}

// For produces the script for one shell and the environment that carries its
// values. Large values are written under dir (0600) and read from there.
func (r RunText) For(shellName, dir string) (string, []string, error) {
	if len(r.Data) == 0 {
		return r.Script, nil, nil
	}
	if shellName == "cmd" {
		return "", nil, fmt.Errorf("a cmd step cannot take values from a template; use pwsh, or read the value from a file the step is given")
	}
	pwsh := shellName == "pwsh" || shellName == "powershell"
	var env []string
	refs := make([]string, len(r.Data))
	total := 0
	for i, v := range r.Data {
		name := "WFX_DATA_" + strconv.Itoa(i+1)
		total += len(v)
		// An environment value cannot hold a NUL byte; such a value goes by file.
		if len(v) <= dataInline && total <= dataBudget && !strings.Contains(v, "\x00") {
			env = append(env, name+"="+v)
			if pwsh {
				refs[i] = "${env:" + name + "}"
			} else {
				refs[i] = "${" + name + "}"
			}
			continue
		}
		if dir == "" {
			return "", nil, fmt.Errorf("value %d is %d bytes and needs a work dir", i+1, len(v))
		}
		path := filepath.Join(dir, "wfx-data-"+strconv.Itoa(i+1))
		if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
			return "", nil, err
		}
		env = append(env, name+"_FILE="+path)
		if pwsh {
			refs[i] = "$(Get-Content -Raw -LiteralPath ${env:" + name + "_FILE})"
		} else {
			refs[i] = `$(cat "${` + name + `_FILE}")`
		}
	}
	return r.replace(func(i int) string { return refs[i] }), env, nil
}

// Display is the script as the run page shows it: references, never values.
func (r RunText) Display() string {
	return r.replace(func(i int) string { return "${WFX_DATA_" + strconv.Itoa(i+1) + "}" })
}

func (r RunText) replace(ref func(int) string) string {
	if !strings.Contains(r.Script, dataMark) {
		return r.Script
	}
	var b strings.Builder
	s := r.Script
	for {
		i := strings.Index(s, dataMark)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+len(dataMark):]
		j := strings.Index(rest, dataEnd)
		n, err := strconv.Atoi(rest[:max(j, 0)])
		if j < 0 || err != nil || n < 1 || n > len(r.Data) {
			b.WriteString(s[i:]) // not ours: leave it as written
			return b.String()
		}
		b.WriteString(ref(n - 1))
		s = rest[j+len(dataEnd):]
	}
}

// CheckRunText is checkRunText for a definition that did not come through the
// loader (built in code): the engine asks again before it runs a step.
func CheckRunText(text, shellName string) error { return checkRunText(text, shellName) }

// checkRunText refuses run text where a value would land inside single quotes
// or a quoted heredoc: there a variable reference is not expanded, so the
// script would see the variable's name instead of the value. Refusing at load
// means nobody finds out at run time.
func checkRunText(text, shellName string) error {
	if !strings.Contains(text, "{{") {
		return nil
	}
	const hole = "\x01"
	parts, err := parseRunText(text)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, p := range parts {
		if p.path == "" {
			b.WriteString(p.text)
		} else {
			b.WriteString(hole)
		}
	}
	skel := b.String()
	if !strings.Contains(skel, hole) {
		return nil
	}
	if shellName == "cmd" {
		return fmt.Errorf("a cmd step cannot take values from a template ({{ … }}); use pwsh, or bash/sh")
	}
	if where := quotedHole(skel, hole); where != "" {
		return fmt.Errorf("a {{ … }} value is inside %s; values reach run steps through the environment, so there it would print a variable name instead of the value — move it outside (for example into double quotes: \"{{ … }}\")", where)
	}
	ctx := make([]byte, len(skel))
	scanShell(skel, hole, ctx)
	if err := evaluatedAgain(skel, hole, ctx); err != nil {
		return err
	}
	return bareValue(skel, hole, ctx)
}

// bareValue refuses a value used as a bare word: outside double quotes (or an
// unquoted heredoc) the shell splits it on spaces, expands globs in it, and a
// value starting with '-' becomes an option of the command.
func bareValue(s, hole string, ctx []byte) error {
	for i := strings.Index(s, hole); i >= 0; {
		if ctx[i] == 'n' || ctx[i] == 'b' {
			return fmt.Errorf("a {{ … }} value is a bare word; wrap it in double quotes (\"{{ … }}\"): unquoted, the shell splits it on spaces, expands globs in it, and a value starting with '-' becomes an option")
		}
		j := strings.Index(s[i+len(hole):], hole)
		if j < 0 {
			break
		}
		i += len(hole) + j
	}
	return nil
}

// cmdWord matches a command word written plainly, quoted ("sh", 'sh') or
// escaped (\sh), which the shell all runs as the same command. The word is
// capture group 1; it must start at a word boundary.
func cmdWord(names string) string {
	return `(?:^|[\s;&|(/])(\\(?:` + names + `)|"(?:` + names + `)"|'(?:` + names + `)'|(?:` + names + `))`
}

// bareWord is a command word without its quotes or escape.
func bareWord(w string) string { return strings.Trim(w, `\"'`) }

var (
	reLet       = regexp.MustCompile(cmdWord(`let`) + `\s`)
	reEval      = regexp.MustCompile(cmdWord(`eval`) + `(?:\s|$)`)
	reSSH       = regexp.MustCompile(cmdWord(`ssh`) + `\s`)
	reTrap      = regexp.MustCompile(cmdWord(`trap`) + `\s`)
	reDeclInt   = regexp.MustCompile(cmdWord(`declare|typeset|local`) + `\s+(?:-[A-Za-z]*\s+)*-[A-Za-z]*i`)
	reInvokeExp = regexp.MustCompile(`(?i)` + cmdWord(`invoke-expression|iex`) + `(?:\s|$)`)
	// The script is the word right after the -c flag (glued or not, after
	// any options and their arguments, and after a `--`).
	reShellC = regexp.MustCompile(cmdWord(shellNames) + `\s+(?:[^\s;&|]+\s+)*?(-[A-Za-z]*c[A-Za-z]*)(?:\s|$|["'])`)
	// Interpreters whose code argument runs as a program: python -c, node -e,
	// perl -e, ruby -e, php -r, … The code is the word after the flag.
	reInterpCode = regexp.MustCompile(cmdWord(`python[0-9.]*|pypy[0-9.]*|node|nodejs|perl|ruby|php|lua|osascript|awk|gawk|mawk|jshell`) + `\s+(?:[^\s;&|]+\s+)*?(-c|-e|-E|-p|-r|-ne|-pe|-le|-lne|-lpe|--eval|--print|--command)(?:\s|=|$|["'])`)
	// PowerShell takes everything after -Command as the command.
	rePwshCommand = regexp.MustCompile(`(?i)` + cmdWord(`pwsh|powershell|pwsh\.exe|powershell\.exe`) + `\s+(?:[^\s;&|]+\s+)*?(-c|-command|-e|-ec|-encodedcommand)(?:\s|$|["'])`)
	// su and runuser take a user before -c.
	reSuC       = regexp.MustCompile(cmdWord(`su|runuser`) + `\s+(?:[^\s;&|]+\s+)*?(-[A-Za-z]*c[A-Za-z]*|--command)(?:\s|=|$|["'])`)
	reSubscript = regexp.MustCompile(`(\$\{[A-Za-z_][A-Za-z0-9_]*|(?:^|[\s;&|(])[A-Za-z_][A-Za-z0-9_]*)\[[^\]\n]*\x01`)
	reNumTest   = regexp.MustCompile(`\s-(eq|ne|lt|le|gt|ge)\s`)
	reDuration  = regexp.MustCompile(`^[0-9][0-9.]*[smhd]?$`)
	arithAdvice = "the shell evaluates what is there as an expression, so a value must be checked first: read it into a variable, check it is a number (for example `case \"$n\" in ''|*[!0-9]*) exit 2;; esac`), then use the variable"
)

// shellNames are the shells (and the variables that name one) a -c script
// or a pipe can be handed to.
const shellNames = `sh|bash|zsh|dash|ksh|mksh|ash|fish|csh|tcsh|busybox|\$\{?BASH\}?|\$\{?SHELL\}?`

// scriptReaders are commands that run what they read on stdin (or from a
// here-string or <( … )) as a script.
var scriptReaders = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "ash": true, "fish": true,
	"csh": true, "tcsh": true, "busybox": true, "$bash": true, "${bash}": true, "$shell": true, "${shell}": true,
	"python": true, "python2": true, "python3": true, "node": true, "perl": true, "ruby": true,
	"php": true, "pwsh": true, "powershell": true, "iex": true, "invoke-expression": true,
	"source": true, ".": true, "ssh": true, "su": true, "runuser": true,
}

// cmdWrappers run the command that follows them. The value lists the options
// that take a separate argument (short letters, and long names).
var cmdWrappers = map[string][]string{
	"env": {"u", "C", "S", "P", "unset", "chdir", "split-string"}, "sudo": {"u", "g", "C", "D", "h", "p", "r", "t", "U", "T", "user", "group", "chdir", "host", "prompt", "role", "type", "other-user", "command-timeout"},
	"doas": {"u", "C"}, "exec": {"a"}, "command": nil, "builtin": nil, "nohup": nil, "time": {"f", "o", "format", "output"},
	"nice": {"n", "adjustment"}, "xargs": {"I", "n", "P", "d", "L", "s", "E", "a", "max-args", "max-procs", "delimiter", "arg-file", "max-chars", "eof", "max-lines"},
	"timeout": {"s", "k", "signal", "kill-after"}, "stdbuf": {"i", "o", "e", "input", "output", "error"},
	"setsid": nil, "chroot": nil, "unbuffer": nil, "ionice": {"c", "n", "p", "class", "classdata", "pid"},
}

// commandName is the command a simple command's text runs: its first word
// after assignments, wrappers and their options (and those options'
// arguments), without quotes, escape, parentheses or directory, lower-cased.
func commandName(seg string) string {
	name, _ := commandSplit(seg)
	return name
}

// commandSplit is commandName plus the words that follow the command.
func commandSplit(seg string) (string, []string) {
	var argOpts []string
	skip := false
	words := strings.Fields(seg)
	for i, w := range words {
		if skip {
			skip = false
			continue
		}
		w = strings.Trim(strings.ToLower(bareWord(w)), "()")
		if k := strings.LastIndexByte(w, '/'); k >= 0 && !strings.HasPrefix(w, "$") {
			w = w[k+1:]
		}
		switch {
		case w == "", strings.Contains(w, "="), reDuration.MatchString(w):
			continue
		case strings.HasPrefix(w, "-"):
			name := strings.TrimLeft(w, "-")
			for _, o := range argOpts {
				if strings.EqualFold(o, name) {
					skip = true
				}
			}
			continue
		}
		if opts, ok := cmdWrappers[w]; ok {
			argOpts = opts
			continue
		}
		return w, words[i+1:]
	}
	return "", nil
}

// readerFlags says, per reader of a script, which short option letters give
// it its code as an argument (so stdin is data), which take a separate
// argument of their own, and which make it read its commands from stdin.
var readerFlags = map[string]struct{ code, withArg, stdin string }{
	"sh": {"c", "oO", "s"}, "bash": {"c", "oO", "s"}, "zsh": {"c", "oO", "s"}, "dash": {"c", "oO", "s"}, "ksh": {"c", "oO", "s"},
	"mksh": {"c", "oO", "s"}, "ash": {"c", "oO", "s"}, "fish": {"c", "", ""}, "csh": {"c", "", "s"}, "tcsh": {"c", "", "s"},
	"$bash": {"c", "oO", "s"}, "${bash}": {"c", "oO", "s"}, "$shell": {"c", "oO", "s"}, "${shell}": {"c", "oO", "s"},
	"python": {"cm", "WX", ""}, "python2": {"cm", "WX", ""}, "python3": {"cm", "WX", ""},
	"node": {"ep", "r", ""}, "perl": {"eE", "", ""}, "ruby": {"e", "Ir", ""}, "php": {"r", "dc", ""},
}

// isRedirect: a shell word that redirects (<f, >f, 2>&1, <<EOF, <<<"…", <( … ))
// rather than being an argument; the bare operators take the next word.
func isRedirect(w string) (redirect, takesNext bool) {
	t := strings.TrimLeft(w, "0123456789&")
	if t == "" || (t[0] != '<' && t[0] != '>') {
		return false, false
	}
	return true, strings.Trim(t, "<>&|") == ""
}

// stdinIsCode: the simple command seg runs what it reads on stdin as a script.
// That is so only when the reader has no script file and no code argument
// (-c, -e, …), or the argument is `-`; given a script or code, what it reads
// on stdin is data.
func stdinIsCode(seg string) (string, bool) {
	name, args := commandSplit(seg)
	if !scriptReaders[name] {
		return "", false
	}
	var words []string
	for i := 0; i < len(args); i++ {
		w := bareWord(args[i])
		if w == "" {
			continue // a line continuation
		}
		if strings.HasPrefix(w, "<(") {
			return name, true // its script is what <( … ) prints
		}
		if r, next := isRedirect(w); r {
			if next {
				i++
			}
			continue
		}
		words = append(words, w)
	}
	switch name {
	case "iex", "invoke-expression":
		return name, true
	case "source", ".":
		return name, len(words) == 0 || words[0] == "-" || words[0] == "/dev/stdin" || words[0] == "/proc/self/fd/0"
	case "ssh":
		// options, then the host; any word after the host is the remote
		// command, and stdin is its data.
		host := false
		for i := 0; i < len(words); i++ {
			w := words[i]
			if strings.HasPrefix(w, "-") {
				if len(w) == 2 && strings.ContainsRune("bcDEeFIiJLlmOopQRSWw", rune(w[1])) {
					i++
				}
				continue
			}
			if host {
				return name, false
			}
			host = true
		}
		return name, true
	case "su", "runuser":
		for _, w := range words {
			if w == "-c" || w == "--command" || strings.HasPrefix(w, "--command=") {
				return name, false
			}
		}
		return name, true
	case "pwsh", "powershell":
		for i := 0; i < len(words); i++ {
			w := strings.ToLower(words[i])
			switch w {
			case "-command", "-c", "-file", "-f", "-encodedcommand", "-ec", "-e":
				return name, i+1 >= len(words) || words[i+1] == "-"
			}
			if !strings.HasPrefix(w, "-") {
				return name, w == "-"
			}
		}
		return name, true
	}
	flags, known := readerFlags[name]
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-":
			return name, true
		case w == "--":
			return name, i+1 >= len(words) || words[i+1] == "-"
		case strings.HasPrefix(w, "--"):
			if w == "--eval" || w == "--print" || w == "--command" || strings.HasPrefix(w, "--eval=") {
				return name, false
			}
		case strings.HasPrefix(w, "-") || strings.HasPrefix(w, "+"):
			if known && strings.ContainsAny(w[1:], flags.code) {
				return name, false
			}
			if known && flags.stdin != "" && strings.ContainsAny(w[1:], flags.stdin) {
				return name, true // -s: commands come from stdin, whatever follows
			}
			if known && flags.withArg != "" && strings.ContainsAny(w[len(w)-1:], flags.withArg) {
				i++
			}
		default:
			return name, false // a script file
		}
	}
	return name, true
}

// afterPipe is where the command that a pipe at i feeds begins: past `|&`,
// spaces, newlines and line continuations.
func afterPipe(s string, i int) int {
	i++
	if i < len(s) && s[i] == '&' {
		i++
	}
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n':
			i++
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '\n':
			i += 2
		default:
			return i
		}
	}
	return i
}

// lineCommands are the simple commands (their text) in the pipeline or list
// on the line around i (split at unquoted |, ;, &).
func lineCommands(s string, ctx []byte, i int) []string {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	// A line continued from the one before (a trailing backslash) belongs to
	// the same command.
	for start >= 2 && s[start-1] == '\n' && s[start-2] == '\\' {
		start = strings.LastIndexByte(s[:start-2], '\n') + 1
	}
	end := strings.IndexByte(s[i:], '\n')
	if end < 0 {
		end = len(s)
	} else {
		end += i
	}
	var out []string
	from := start
	for k := start; k <= end; k++ {
		if k == end || (ctx[k] == 'n' && strings.IndexByte("|;&", s[k]) >= 0) {
			if strings.TrimSpace(s[from:k]) != "" {
				out = append(out, s[from:k])
			}
			from = k + 1
		}
	}
	return out
}

// readsScript names the first of the commands that runs its stdin as code.
func readsScript(segs []string) string {
	for _, seg := range segs {
		if name, ok := stdinIsCode(seg); ok {
			return name
		}
	}
	return ""
}

// evaluatedAgain refuses a value in a place where the shell evaluates text a
// second time: arithmetic ($(( )), (( )), let, declare -i, a numeric [[ ]]
// comparison, array subscripts), the text handed to eval, trap, `sh -c` and
// its relatives, su -c, ssh or Invoke-Expression, and text fed to a shell or
// interpreter through a pipe, a heredoc, a here-string or <( … ).
func evaluatedAgain(s, hole string, ctx []byte) error {
	plain := func(i int) bool { return i >= 0 && i < len(ctx) && ctx[i] == 'n' }
	open := func(i int) bool { return i < len(ctx) && (ctx[i] == 'n' || ctx[i] == 'd') }
	// $(( … )) and (( … )): find the matching close by paren depth.
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '(' || s[i+1] != '(' {
			continue
		}
		dollar := i > 0 && s[i-1] == '$'
		if (dollar && !open(i-1)) || (!dollar && !plain(i)) {
			continue
		}
		depth, j := 2, i+2
		for ; j < len(s) && depth > 0; j++ {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if strings.Contains(s[i:j], hole) {
			return fmt.Errorf("a {{ … }} value is inside shell arithmetic (( … )); %s", arithAdvice)
		}
		i = j - 1
	}
	// An array subscript is arithmetic too: ${a[…]} anywhere it expands, and
	// a[…]=… as an assignment.
	for _, m := range reSubscript.FindAllStringSubmatchIndex(s, -1) {
		if s[m[2]] == '$' {
			if open(m[2]) {
				return fmt.Errorf("a {{ … }} value is in an array subscript ${…[ … ]}; %s", arithAdvice)
			}
			continue
		}
		name := m[2] + strings.IndexFunc(s[m[2]:m[3]], func(r rune) bool { return !strings.ContainsRune(" \t\n;&|(", r) })
		if plain(name) && assignAfter(s, m[1]) {
			return fmt.Errorf("a {{ … }} value is in an array subscript a[ … ]=; %s", arithAdvice)
		}
	}
	// inCommand: the command word at group 1 is really a command (unquoted
	// context) and its command text holds a value.
	inCommand := func(re *regexp.Regexp) bool {
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			if plain(m[2]) && strings.Contains(s[m[2]:commandEnd(s, ctx, m[3])], hole) {
				return true
			}
		}
		return false
	}
	if inCommand(reLet) {
		return fmt.Errorf("a {{ … }} value is in a let expression; %s", arithAdvice)
	}
	if inCommand(reDeclInt) {
		return fmt.Errorf("a {{ … }} value is assigned to an integer variable (declare/typeset/local -i), which evaluates it as arithmetic; %s", arithAdvice)
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '[' && s[i+1] == '[' && plain(i) {
			j := strings.Index(s[i:], "]]")
			if j < 0 {
				break
			}
			seg := s[i : i+j]
			if strings.Contains(seg, hole) && reNumTest.MatchString(seg) {
				return fmt.Errorf("a {{ … }} value is in a numeric [[ … ]] comparison; %s", arithAdvice)
			}
			i += j
		}
	}
	if inCommand(reEval) {
		return fmt.Errorf("a {{ … }} value is in text passed to eval, which runs it as a script; pass it as data instead (for example in an environment variable or a file the script reads)")
	}
	if inCommand(reTrap) {
		return fmt.Errorf("a {{ … }} value is in a trap action, which the shell runs as a script later; set a variable to the value and refer to the variable inside a single-quoted action: trap 'echo \"$v\"' EXIT")
	}
	if inCommand(reInvokeExp) {
		return fmt.Errorf("a {{ … }} value is in text passed to Invoke-Expression, which runs it as a script; pass it as data instead")
	}
	if inCommand(reSSH) {
		return fmt.Errorf("a {{ … }} value is in an ssh command, whose remote shell runs it as a script; copy it over as a file, or check it first")
	}
	for _, re := range []*regexp.Regexp{reShellC, reSuC} {
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			if !plain(m[2]) {
				continue
			}
			// The script is the word after -c (past a `--`); later words are
			// its arguments ($0, $1, …) and stay data.
			from := m[5]
			if end := wordEnd(s, ctx, from); strings.TrimSpace(s[from:end]) == "--" {
				from = end
			}
			if end := wordEnd(s, ctx, from); strings.Contains(s[from:end], hole) {
				w := bareWord(s[m[2]:m[3]])
				return fmt.Errorf("a {{ … }} value is in the script passed to %s -c, which runs it again as a script; pass it as an argument instead: %s -c 'echo \"$1\"' _ \"{{ … }}\"", w, w)
			}
		}
	}
	for _, m := range reInterpCode.FindAllStringSubmatchIndex(s, -1) {
		if !plain(m[2]) {
			continue
		}
		from := m[5]
		if from < len(s) && s[from] == '=' {
			from++
		}
		if end := wordEnd(s, ctx, from); strings.Contains(s[from:end], hole) {
			w := bareWord(s[m[2]:m[3]])
			return fmt.Errorf("a {{ … }} value is in the code passed to %s %s, which runs it as a program; pass it as an argument after the code (sys.argv, process.argv, @ARGV, ARGV) or in an environment variable instead", w, s[m[4]:m[5]])
		}
	}
	for _, m := range rePwshCommand.FindAllStringSubmatchIndex(s, -1) {
		if plain(m[2]) && strings.Contains(s[m[5]:commandEnd(s, ctx, m[5])], hole) {
			return fmt.Errorf("a {{ … }} value is in the command passed to %s %s, which runs it as a script; pass it in an environment variable instead", bareWord(s[m[2]:m[3]]), s[m[4]:m[5]])
		}
	}
	// Text fed to a shell or interpreter as its script: a pipe into it, a
	// heredoc or here-string it reads, or <( … ) it is given.
	for i := 0; i < len(s); i++ {
		if !plain(i) {
			continue
		}
		switch {
		case s[i] == '|' && !strings.HasPrefix(s[i:], "||") && (i == 0 || s[i-1] != '|'):
			at := afterPipe(s, i)
			if r, code := stdinIsCode(s[at:commandEnd(s, ctx, at)]); code {
				start := i
				for start > 0 && !(plain(start-1) && strings.IndexByte(";&\n", s[start-1]) >= 0) {
					start--
				}
				if strings.Contains(s[start:i], hole) {
					return fmt.Errorf("a {{ … }} value is piped into %s, which runs what it reads as a script; write the value to a file, or pass it as an argument or environment variable", r)
				}
			}
		case strings.HasPrefix(s[i:], "<<<"), strings.HasPrefix(s[i:], "<("):
			if r := readsScript(lineCommands(s, ctx, i)); r != "" {
				end := strings.IndexByte(s[i:], '\n')
				if end < 0 {
					end = len(s) - i
				}
				if strings.Contains(s[i:i+end], hole) {
					return fmt.Errorf("a {{ … }} value is given to %s as a script (here-string or <( … )), which runs it; pass it as data instead", r)
				}
			}
			if strings.HasPrefix(s[i:], "<<<") {
				i += 2
			}
		case strings.HasPrefix(s[i:], "<<"):
			if r := readsScript(lineCommands(s, ctx, i)); r != "" {
				if body := heredocBody(s, i); strings.Contains(body, hole) {
					return fmt.Errorf("a {{ … }} value is in a heredoc that %s reads as a script, which runs it; write the value to a file the script reads, or pass it as an argument or environment variable", r)
				}
			}
			i++
		}
	}
	return nil
}

// heredocBody is the body of the heredoc whose << starts at i: the lines after
// the command's line, up to the delimiter line.
func heredocBody(s string, i int) string {
	j := i + 2
	strip := j < len(s) && s[j] == '-'
	if strip {
		j++
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	k := j
	for k < len(s) && !strings.ContainsRune(" \t\n;&|<>()", rune(s[k])) {
		k++
	}
	word := strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(s[j:k])
	nl := strings.IndexByte(s[i:], '\n')
	if nl < 0 || word == "" {
		return ""
	}
	start := i + nl + 1
	for p := start; p < len(s); {
		e := strings.IndexByte(s[p:], '\n')
		line := s[p:]
		if e >= 0 {
			line = s[p : p+e]
		}
		probe := line
		if strip {
			probe = strings.TrimLeft(probe, "\t")
		}
		if probe == word {
			return s[start:p]
		}
		if e < 0 {
			break
		}
		p += e + 1
	}
	return s[start:]
}

// assignAfter: the subscript holding the hole that ends at i closes with "]=".
func assignAfter(s string, i int) bool {
	j := strings.IndexByte(s[i:], ']')
	return j >= 0 && strings.HasPrefix(s[i+j:], "]=")
}

// commandEnd is where the simple command running from i ends: the first
// unquoted ;, &, |, or newline.
func commandEnd(s string, ctx []byte, i int) int {
	for ; i < len(s); i++ {
		if ctx[i] == 'n' && strings.IndexByte(";&|\n", s[i]) >= 0 {
			return i
		}
	}
	return len(s)
}

// wordEnd is where the shell word starting at i (after spaces) ends.
func wordEnd(s string, ctx []byte, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for ; i < len(s); i++ {
		if ctx[i] == 'n' && strings.IndexByte(" \t;&|\n", s[i]) >= 0 {
			return i
		}
	}
	return len(s)
}

// quotedHole scans shell text (POSIX quoting, which pwsh shares for single
// quotes) and says where a hole sits in a context that does not expand
// variables: "single quotes" or "a quoted heredoc (<<'EOF')". "" when none.
func quotedHole(s, hole string) string { return scanShell(s, hole, nil) }

// scanShell is quotedHole that also records, when ctx is given, the quoting
// context of every byte: 'n' unquoted (top level or inside $( … )), 'd' double
// quotes, 's' single quotes, 'b' backticks, 'h' a heredoc body.
func scanShell(s, hole string, ctx []byte) string {
	const (
		normal = iota
		dq
		sq
		bq
	)
	type frame struct{ kind, depth int }
	stack := []frame{{normal, 0}}
	type heredoc struct {
		word   string
		strip  bool
		quoted bool
	}
	var pending []heredoc
	atWordStart := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		top := &stack[len(stack)-1]
		if ctx != nil {
			ctx[i] = "ndsb"[top.kind]
		}
		if strings.HasPrefix(s[i:], hole) && top.kind == sq {
			return "single quotes"
		}
		switch top.kind {
		case sq:
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
			continue
		case dq:
			switch {
			case c == '\\':
				i++
			case c == '"':
				stack = stack[:len(stack)-1]
			case c == '$' && i+1 < len(s) && s[i+1] == '(':
				stack = append(stack, frame{normal, 1})
				i++
			case c == '`':
				stack = append(stack, frame{bq, 0})
			}
			continue
		case bq:
			if c == '\\' {
				i++
			} else if c == '`' {
				stack = stack[:len(stack)-1]
			} else if c == '\'' {
				stack = append(stack, frame{sq, 0})
			} else if c == '"' {
				stack = append(stack, frame{dq, 0})
			}
			continue
		}
		// normal (top level or inside $( … ))
		switch {
		case c == '\\':
			i++
			atWordStart = false
		case c == '\'': // also $'…', which expands no variables either
			stack = append(stack, frame{sq, 0})
		case c == '"':
			stack = append(stack, frame{dq, 0})
		case c == '`':
			stack = append(stack, frame{bq, 0})
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			stack = append(stack, frame{normal, 1})
			i++
		case c == '(' && len(stack) > 1:
			top.depth++
		case c == ')' && len(stack) > 1:
			top.depth--
			if top.depth == 0 {
				stack = stack[:len(stack)-1]
			}
		case c == '#' && atWordStart:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i-- // let the newline be handled below
		case c == '<' && strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<"):
			j := i + 2
			h := heredoc{}
			if j < len(s) && s[j] == '-' {
				h.strip = true
				j++
			}
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			k := j
			for k < len(s) && !strings.ContainsRune(" \t\n;&|<>()", rune(s[k])) {
				k++
			}
			raw := s[j:k]
			h.quoted = strings.ContainsAny(raw, `'"\`)
			h.word = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(raw)
			if h.word != "" {
				pending = append(pending, h)
			}
			i = k - 1
		case c == '\n' && len(stack) == 1 && len(pending) > 0:
			// Heredoc bodies follow this line, one after another.
			i++
			for _, h := range pending {
				for i < len(s) {
					if ctx != nil {
						// A quoted heredoc expands nothing ('h'); an unquoted
						// one expands like double quotes ('d').
						kind := byte('d')
						if h.quoted {
							kind = 'h'
						}
						for k := i; k < len(s) && s[k] != '\n'; k++ {
							ctx[k] = kind
						}
					}
					end := strings.IndexByte(s[i:], '\n')
					line := s[i:]
					if end >= 0 {
						line = s[i : i+end]
					}
					probe := line
					if h.strip {
						probe = strings.TrimLeft(probe, "\t")
					}
					if probe == h.word {
						if end < 0 {
							i = len(s)
						} else {
							i += end + 1
						}
						break
					}
					if h.quoted && strings.Contains(line, hole) && ctx == nil {
						return "a quoted heredoc (<<'" + h.word + "')"
					}
					if end < 0 {
						i = len(s)
						break
					}
					i += end + 1
				}
			}
			pending = nil
			i--
		}
		atWordStart = c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '('
	}
	return ""
}
