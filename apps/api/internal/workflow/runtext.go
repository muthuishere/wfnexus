package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// What still prints into the text: the run's own facts (.RunID, .WorkDir,
// .BaseRef, .Index), which the platform makes — but only where the dot is the
// run's own data: at the top level, or written $.RunID anywhere. Inside a
// `range` or `with` the dot is a value (a loop item, an input object), so a
// field of it named RunID is that value's and travels as data like any other.
// Conditions (`{{ if … }}`,
// `{{ range … }}`) still read the values directly, so branching is unchanged.
//
// Prompts, gate messages and every other non-shell text keep plain
// substitution (Render).

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

// platformFacts are the fields printed into run text as they are.
var platformFacts = map[string]bool{"RunID": true, "WorkDir": true, "BaseRef": true, "Index": true}

// RenderRun renders a `run:` step's text with every printed value replaced by
// a mark (see RunText.For).
func RenderRun(text string, data TemplateData) (RunText, error) {
	var vals []string
	t, err := parseRunTemplate(text, func(v any) string {
		s := ""
		if v != nil {
			s = fmt.Sprint(v)
		}
		if s == "<no value>" {
			s = ""
		}
		vals = append(vals, s)
		return dataMark + strconv.Itoa(len(vals)) + dataEnd
	})
	if err != nil {
		return RunText{}, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return RunText{}, err
	}
	return RunText{Script: strings.ReplaceAll(buf.String(), "<no value>", ""), Data: vals}, nil
}

// parseRunTemplate parses run text and routes every value-printing action
// through wfxData.
func parseRunTemplate(text string, wfxData func(any) string) (*template.Template, error) {
	t, err := template.New("run").Funcs(funcs).Funcs(template.FuncMap{"wfxData": wfxData}).
		Option("missingkey=zero").Parse(rewriteStatePaths(rewriteStepPaths(text)))
	if err != nil {
		return nil, err
	}
	if len(t.Templates()) > 1 || hasTemplateCall(t.Tree) {
		return nil, errRunNamedTemplate
	}
	if t.Tree != nil {
		walkActions(t.Tree.Root, true, func(a *parse.ActionNode, root bool) {
			if isPlatformFact(a.Pipe, root) {
				return
			}
			id := parse.NewIdentifier("wfxData").SetTree(t.Tree).SetPos(a.Pos)
			a.Pipe.Cmds = append(a.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Pos: a.Pos, Args: []parse.Node{id}})
		})
	}
	return t, nil
}

// errRunNamedTemplate: run text may not define or call named templates
// (define, template, block). Their bodies are separate trees, and a value they
// print must travel like every other one, so they are refused.
var errRunNamedTemplate = errors.New("run text may not use define, template or block; write the text in place (prompts may still use them)")

func hasTemplateCall(t *parse.Tree) bool {
	if t == nil {
		return false
	}
	found := false
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n != nil {
				for _, c := range n.Nodes {
					walk(c)
				}
			}
		case *parse.TemplateNode:
			found = true
		case *parse.IfNode:
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.List)
			walk(n.ElseList)
		}
	}
	walk(t.Root)
	return found
}

// walkActions calls f on every action that prints (not one that only
// declares a variable), inside conditions, loops and withs too. root says
// whether the dot there is still the run's own data: the body of a range or
// with moves the dot onto a value; their else branches keep it.
func walkActions(n parse.Node, root bool, f func(*parse.ActionNode, bool)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			walkActions(c, root, f)
		}
	case *parse.ActionNode:
		if len(n.Pipe.Decl) == 0 {
			f(n, root)
		}
	case *parse.IfNode:
		walkActions(n.List, root, f)
		walkActions(n.ElseList, root, f)
	case *parse.RangeNode:
		walkActions(n.List, false, f)
		walkActions(n.ElseList, root, f)
	case *parse.WithNode:
		walkActions(n.List, false, f)
		walkActions(n.ElseList, root, f)
	}
}

// isPlatformFact: the action prints exactly one of the run's own facts — .RunID
// (and the others) where the dot is the run's data, or $.RunID anywhere. A
// field of that name under any other dot belongs to a value.
func isPlatformFact(p *parse.PipeNode, root bool) bool {
	if p == nil || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return false
	}
	switch a := p.Cmds[0].Args[0].(type) {
	case *parse.FieldNode:
		return root && len(a.Ident) == 1 && platformFacts[a.Ident[0]]
	case *parse.VariableNode:
		return len(a.Ident) == 2 && a.Ident[0] == "$" && platformFacts[a.Ident[1]]
	}
	return false
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
	t, err := parseRunTemplate(text, func(any) string { return "" })
	if errors.Is(err, errRunNamedTemplate) {
		return err
	}
	if err != nil {
		return nil // a bad template is reported where it renders
	}
	var b strings.Builder
	flatten(t.Tree.Root, true, &b, hole)
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
	// The script is the word right after the -c flag (glued or not).
	reShellC = regexp.MustCompile(cmdWord(`sh|bash|zsh|dash|ksh`) + `\s+(?:-[A-Za-z-]*\s+)*(-[A-Za-z]*c[A-Za-z]*)(?:\s|$|["'])`)
	// su and runuser take a user before -c.
	reSuC       = regexp.MustCompile(cmdWord(`su|runuser`) + `\s+(?:[^\s;&|]+\s+)*?(-[A-Za-z]*c[A-Za-z]*|--command)(?:\s|=|$|["'])`)
	reSubscript = regexp.MustCompile(`(\$\{[A-Za-z_][A-Za-z0-9_]*|(?:^|[\s;&|(])[A-Za-z_][A-Za-z0-9_]*)\[[^\]\n]*\x01`)
	reNumTest   = regexp.MustCompile(`\s-(eq|ne|lt|le|gt|ge)\s`)
	reDuration  = regexp.MustCompile(`^[0-9.]+[smhd]?$`)
	arithAdvice = "the shell evaluates what is there as an expression, so a value must be checked first: read it into a variable, check it is a number (for example `case \"$n\" in ''|*[!0-9]*) exit 2;; esac`), then use the variable"
)

// scriptReaders are commands that run what they read on stdin (or from a
// here-string or <( … )) as a script.
var scriptReaders = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "fish": true,
	"python": true, "python2": true, "python3": true, "node": true, "perl": true, "ruby": true,
	"php": true, "pwsh": true, "powershell": true, "iex": true, "invoke-expression": true,
	"source": true, ".": true, "ssh": true, "su": true, "runuser": true,
}

// cmdWrappers run the command that follows them.
var cmdWrappers = map[string]bool{
	"env": true, "sudo": true, "doas": true, "exec": true, "command": true, "builtin": true,
	"nohup": true, "time": true, "nice": true, "xargs": true, "timeout": true, "stdbuf": true,
}

// commandName is the command a simple command's text runs: its first word
// after assignments, wrappers and their options, without quotes, escape or
// directory, lower-cased.
func commandName(seg string) string {
	for _, w := range strings.Fields(seg) {
		w = strings.ToLower(bareWord(w))
		if k := strings.LastIndexByte(w, '/'); k >= 0 {
			w = w[k+1:]
		}
		switch {
		case w == "", strings.HasPrefix(w, "-"), strings.Contains(w, "="), reDuration.MatchString(w):
			continue
		case cmdWrappers[w]:
			continue
		}
		return w
	}
	return ""
}

// lineCommands are the names of the commands in the pipeline or list on the
// line around i (split at unquoted |, ;, &).
func lineCommands(s string, ctx []byte, i int) []string {
	start := strings.LastIndexByte(s[:i], '\n') + 1
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
			if name := commandName(s[from:k]); name != "" {
				out = append(out, name)
			}
			from = k + 1
		}
	}
	return out
}

func readsScript(names []string) string {
	for _, n := range names {
		if scriptReaders[n] {
			return n
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
			// The script is the word after -c; later words are its arguments
			// ($0, $1, …) and stay data.
			if end := wordEnd(s, ctx, m[5]); strings.Contains(s[m[5]:end], hole) {
				w := bareWord(s[m[2]:m[3]])
				return fmt.Errorf("a {{ … }} value is in the script passed to %s -c, which runs it again as a script; pass it as an argument instead: %s -c 'echo \"$1\"' _ \"{{ … }}\"", w, w)
			}
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
			if r := commandName(s[i+1 : commandEnd(s, ctx, i+1)]); scriptReaders[r] {
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

// flatten writes the run text with every value-printing action as hole, in
// source order (both branches of a condition).
func flatten(n parse.Node, root bool, b *strings.Builder, hole string) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			flatten(c, root, b, hole)
		}
	case *parse.TextNode:
		b.Write(n.Text)
	case *parse.ActionNode:
		if len(n.Pipe.Decl) > 0 {
			return
		}
		if isPlatformFact(n.Pipe, root) {
			b.WriteString("x")
		} else {
			b.WriteString(hole)
		}
	case *parse.IfNode:
		flatten(n.List, root, b, hole)
		flatten(n.ElseList, root, b, hole)
	case *parse.RangeNode:
		flatten(n.List, false, b, hole)
		flatten(n.ElseList, root, b, hole)
	case *parse.WithNode:
		flatten(n.List, false, b, hole)
		flatten(n.ElseList, root, b, hole)
	}
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
