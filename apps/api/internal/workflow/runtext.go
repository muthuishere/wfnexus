package workflow

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
// arrives byte for byte, whatever quotes, newlines or shell syntax it holds,
// and the script's text is only what the workflow author wrote.
//
// What still prints into the text: the run's own facts (.RunID, .WorkDir,
// .BaseRef, .Index), which the platform makes. Conditions (`{{ if … }}`,
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
	if t.Tree != nil {
		walkActions(t.Tree.Root, func(a *parse.ActionNode) {
			if isPlatformFact(a.Pipe) {
				return
			}
			id := parse.NewIdentifier("wfxData").SetTree(t.Tree).SetPos(a.Pos)
			a.Pipe.Cmds = append(a.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Pos: a.Pos, Args: []parse.Node{id}})
		})
	}
	return t, nil
}

// walkActions calls f on every action that prints (not one that only
// declares a variable), inside conditions, loops and withs too.
func walkActions(n parse.Node, f func(*parse.ActionNode)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			walkActions(c, f)
		}
	case *parse.ActionNode:
		if len(n.Pipe.Decl) == 0 {
			f(n)
		}
	case *parse.IfNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	case *parse.RangeNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	case *parse.WithNode:
		walkActions(n.List, f)
		walkActions(n.ElseList, f)
	}
}

func isPlatformFact(p *parse.PipeNode) bool {
	if p == nil || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return false
	}
	fn, ok := p.Cmds[0].Args[0].(*parse.FieldNode)
	return ok && len(fn.Ident) == 1 && platformFacts[fn.Ident[0]]
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
		if len(v) <= dataInline && total <= dataBudget {
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
	if err != nil {
		return nil // a bad template is reported where it renders
	}
	var b strings.Builder
	flatten(t.Tree.Root, &b, hole)
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
	return nil
}

// flatten writes the run text with every value-printing action as hole, in
// source order (both branches of a condition).
func flatten(n parse.Node, b *strings.Builder, hole string) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			flatten(c, b, hole)
		}
	case *parse.TextNode:
		b.Write(n.Text)
	case *parse.ActionNode:
		if len(n.Pipe.Decl) > 0 {
			return
		}
		if isPlatformFact(n.Pipe) {
			b.WriteString("x")
		} else {
			b.WriteString(hole)
		}
	case *parse.IfNode:
		flatten(n.List, b, hole)
		flatten(n.ElseList, b, hole)
	case *parse.RangeNode:
		flatten(n.List, b, hole)
		flatten(n.ElseList, b, hole)
	case *parse.WithNode:
		flatten(n.List, b, hole)
		flatten(n.ElseList, b, hole)
	}
}

// quotedHole scans shell text (POSIX quoting, which pwsh shares for single
// quotes) and says where a hole sits in a context that does not expand
// variables: "single quotes" or "a quoted heredoc (<<'EOF')". "" when none.
func quotedHole(s, hole string) string {
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
					if h.quoted && strings.Contains(line, hole) {
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
