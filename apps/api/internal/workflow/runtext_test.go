package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filled is a run text as the script receives it once its values are in
// place: what a test about substitution compares.
func filled(t *testing.T, run string, input map[string]any) string {
	t.Helper()
	rt, err := RenderRun(run, TemplateData{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return rt.replace(func(i int) string { return rt.Data[i] })
}

const awkward = "a \"b\" 'c'\n$(touch x) `id` ${HOME} \\ \nWFX_V\nend"

func TestRunValuesTravelAsDataNotText(t *testing.T) {
	rt, err := RenderRun(`echo "{{ .Input.who }}" {{ .Steps.a.out }} {{ json .Input.list }}`,
		TemplateData{Input: map[string]any{"who": awkward, "list": []any{"x"}}, Steps: map[string]any{"a": map[string]any{"out": "o"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Data) != 3 || rt.Data[0] != awkward || rt.Data[1] != "o" || !strings.Contains(rt.Data[2], `"x"`) {
		t.Fatalf("data = %q", rt.Data)
	}
	if strings.Contains(rt.Script, "touch") || strings.Contains(rt.Script, "WFX_V") {
		t.Fatalf("a value reached the script text: %q", rt.Script)
	}
	script, env, err := rt.For("bash", "")
	if err != nil {
		t.Fatal(err)
	}
	if script != `echo "${WFX_DATA_1}" ${WFX_DATA_2} ${WFX_DATA_3}` {
		t.Fatalf("script = %q", script)
	}
	if env[0] != "WFX_DATA_1="+awkward {
		t.Fatalf("env = %q", env)
	}
	if rt.Display() != script {
		t.Fatalf("display = %q", rt.Display())
	}
}

// Every runtime source is treated alike: input, step outputs, the current
// output, judge answers, loop items, the four state scopes and memory.
func TestEveryRuntimeSourceIsData(t *testing.T) {
	data := TemplateData{
		Input: map[string]any{"v": "I"}, Steps: map[string]any{"s": map[string]any{"v": "S"}},
		Output: map[string]any{"v": "O"}, Decide: map[string]any{"v": "D"}, Item: "IT",
		Step: map[string]string{"v": "ST"}, Workflow: map[string]string{"v": "W"},
		Project: map[string]string{"v": "P"}, Global: map[string]string{"v": "G"}, Memory: "M",
	}
	rt, err := RenderRun(`{{ .Input.v }}{{ .Steps.s.v }}{{ .Output.v }}{{ .Decide.v }}{{ .Item }}{{ .Step.v }}{{ .Workflow.v }}{{ .Project.v }}{{ .Global.v }}{{ .Memory }}{{ range $k, $x := .Input }}{{ $x }}{{ end }}{{ with .Input.v }}{{ . }}{{ end }}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"I", "S", "O", "D", "IT", "ST", "W", "P", "G", "M", "I", "I"}; strings.Join(rt.Data, ",") != strings.Join(want, ",") {
		t.Fatalf("data = %q", rt.Data)
	}
	if strings.Trim(rt.Display(), "${}WFX_DATA_0123456789") != "" {
		t.Fatalf("script still holds text: %q", rt.Display())
	}
}

// Conditions read the values themselves, so branching is unchanged; the
// platform's own facts print as they are.
func TestConditionsAndPlatformFactsAreUnchanged(t *testing.T) {
	rt, err := RenderRun(`{{ if eq .Input.mode "fast" }}--fast{{ end }} cd '{{ .WorkDir }}' && echo {{ .RunID }} {{ .Index }}{{ $x := .Input.mode }}`,
		TemplateData{Input: map[string]any{"mode": "fast"}, WorkDir: "/w", RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Data) != 0 || rt.Script != "--fast cd '/w' && echo r1 0" {
		t.Fatalf("script %q data %q", rt.Script, rt.Data)
	}
	if rt, _ := RenderRun(`x{{ .Input.missing }}y`, TemplateData{Input: map[string]any{}}); filled(t, `x{{ .Input.missing }}y`, nil) != "xy" || len(rt.Data) != 1 {
		t.Fatalf("missing = %q", rt.Data)
	}
}

func TestLargeValuesGoToAFile(t *testing.T) {
	big := strings.Repeat("z", dataInline+1)
	rt, _ := RenderRun(`wc -c <<EOF
{{ .Input.big }}
EOF
echo "{{ .Input.small }}"`, TemplateData{Input: map[string]any{"big": big, "small": "s"}})
	dir := t.TempDir()
	script, env, err := rt.For("sh", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `$(cat "${WFX_DATA_1_FILE}")`) || !strings.Contains(script, `"${WFX_DATA_2}"`) {
		t.Fatalf("script = %q", script)
	}
	path := filepath.Join(dir, "wfx-data-1")
	if env[0] != "WFX_DATA_1_FILE="+path || env[1] != "WFX_DATA_2=s" {
		t.Fatalf("env = %q", env)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() != int64(len(big)) {
		t.Fatalf("file %v %v", st, err)
	}
	if _, _, err := rt.For("sh", ""); err == nil {
		t.Fatal("a large value with no dir was accepted")
	}
}

// An environment value cannot hold a NUL byte, so such a value goes by file.
func TestANulByteGoesByFile(t *testing.T) {
	rt, _ := RenderRun(`printf '%s' "{{ .Input.v }}"`, TemplateData{Input: map[string]any{"v": "a\x00b"}})
	dir := t.TempDir()
	script, env, err := rt.For("sh", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "WFX_DATA_1_FILE") || env[0] != "WFX_DATA_1_FILE="+filepath.Join(dir, "wfx-data-1") {
		t.Fatalf("script %q env %q", script, env)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "wfx-data-1")); string(b) != "a\x00b" {
		t.Fatalf("file = %q", b)
	}
}

// RenderRun refuses what checkRunText refuses: named templates.
func TestRenderRunRefusesNamedTemplates(t *testing.T) {
	if _, err := RenderRun(`{{ define "t" }}echo "{{ .Input.x }}"{{ end }}{{ template "t" . }}`, TemplateData{}); err == nil {
		t.Fatal("rendered a named template")
	}
}

func TestShellsReferToValuesTheirOwnWay(t *testing.T) {
	rt, _ := RenderRun(`echo "{{ .Input.v }}"`, TemplateData{Input: map[string]any{"v": "x"}})
	if s, _, _ := rt.For("pwsh", ""); s != `echo "${env:WFX_DATA_1}"` {
		t.Fatalf("pwsh = %q", s)
	}
	if _, _, err := rt.For("cmd", ""); err == nil {
		t.Fatal("cmd took a value")
	}
	if s, env, _ := (RunText{Script: "echo hi"}).For("cmd", ""); s != "echo hi" || env != nil {
		t.Fatal("cmd without values changed")
	}
}

// Variable names are positional (WFX_DATA_<n>), so no input name has to map
// onto one: names that would clash as variables never meet.
func TestVariableNamesDoNotDependOnInputNames(t *testing.T) {
	rt, _ := RenderRun(`{{ index .Input "pr-number" }} {{ .Input.PR_NUMBER }} {{ index .Input "pr number" }}`,
		TemplateData{Input: map[string]any{"pr-number": "1", "PR_NUMBER": "2", "pr number": "3"}})
	_, env, _ := rt.For("sh", "")
	if strings.Join(env, ",") != "WFX_DATA_1=1,WFX_DATA_2=2,WFX_DATA_3=3" {
		t.Fatalf("env = %q", env)
	}
}

func TestRunTextCheck(t *testing.T) {
	for _, c := range []struct {
		run, shell, want string
	}{
		{`echo "{{ .Input.x }}"`, "", ""},
		{`echo {{ .Input.x }}`, "", ""},
		{"cat > f <<EOF\n{{ .Input.x }}\nEOF", "", ""},
		{`echo '{{ .WorkDir }}'`, "", ""},
		{`echo '{{ .Input.x }}'`, "", "single quotes"},
		{`echo "$(echo '{{ .Input.x }}')"`, "", "single quotes"},
		{"cat > f <<'EOF'\n{{ .Input.x }}\nEOF", "", "quoted heredoc"},
		{"cat > f <<-\"EOF\"\n\t{{ .Input.x }}\n\tEOF\necho done", "", "quoted heredoc"},
		{"cat > f <<'EOF'\nstatic\nEOF\necho \"{{ .Input.x }}\"", "", ""},
		{"# it's a comment\necho \"{{ .Input.x }}\"", "", ""},
		{`echo 'a' "{{ .Input.x }}" 'b'`, "", ""},
		{`{{ if .Input.x }}'flag'{{ end }}`, "", ""},
		{`echo {{ .Input.x }}`, "cmd", "cmd step"},
		{`echo '{{ .Input.x }}'`, "pwsh", "single quotes"},
		// named templates are separate trees: refused
		{`{{ define "t" }}echo "{{ .Input.x }}"{{ end }}{{ template "t" . }}`, "", "define, template or block"},
		{`{{ block "b" . }}echo "{{ .Input.x }}"{{ end }}`, "", "define, template or block"},
		// places the shell evaluates text again
		{`echo $(( {{ .Input.x }} + 1 ))`, "", "arithmetic"},
		{`echo "$(( {{ .Input.x }} + 1 ))"`, "", "arithmetic"},
		{`(( {{ .Input.x }} > 1 )) && echo y`, "", "arithmetic"},
		{"cat <<EOF\n$(( {{ .Input.x }} ))\nEOF", "", "arithmetic"},
		{`let n={{ .Input.x }}+1`, "", "let"},
		{`[[ {{ .Input.x }} -eq 1 ]] && echo y`, "", "numeric"},
		{`[[ "{{ .Input.x }}" -gt 1 ]]`, "", "numeric"},
		{`echo "${arr[{{ .Input.x }}]}"`, "", "subscript"},
		{`arr[{{ .Input.x }}]=1`, "", "subscript"},
		{`sh -c "echo {{ .Input.x }}"`, "", "sh -c"},
		{`bash -c "echo {{ .Input.x }}"`, "", "bash -c"},
		{`/bin/bash -lc "echo {{ .Input.x }}"`, "", "bash -c"},
		{`zsh -c "echo {{ .Input.x }}"`, "", "zsh -c"},
		{`eval "echo {{ .Input.x }}"`, "", "eval"},
		{`ssh host "ls {{ .Input.x }}"`, "", "ssh"},
		// the same words where nothing is evaluated again
		{`sh -c 'echo "$1"' _ "{{ .Input.x }}"`, "", ""},
		{`[[ "{{ .Input.x }}" == yes ]] && echo y`, "", ""},
		{`echo "eval {{ .Input.x }}"`, "", ""},
		{`echo "sh -c {{ .Input.x }}"`, "", ""},
		{`n=$(( 1 + 2 )); echo "{{ .Input.x }}"`, "", ""},
		{`grep "a[{{ .Input.x }}]" f`, "", ""},
	} {
		err := checkRunText(c.run, c.shell)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q refused: %v", c.run, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q: got %v, want %q", c.run, err, c.want)
		}
	}
}

// A workflow that puts a value where it would no longer arrive is refused
// when it loads, naming the workflow and the step.
func TestAQuotedValueRefusesTheWorkflowAtLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "q.yaml"), "name: q\nsteps:\n  - id: greet\n    run: echo '{{ .Input.who }}'\n")
	_, err := LoadDir(dir, catalog())
	if err == nil || !strings.Contains(err.Error(), "q/greet") || !strings.Contains(err.Error(), "single quotes") {
		t.Fatalf("err = %v", err)
	}
}

// Every workflow, template and module shipped in this repository passes the
// check, so none of them is refused after this change.
func TestShippedRunTextPassesTheCheck(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	n := 0
	for _, glob := range []string{"workflows/*/workflow.yaml", "workflows/*.yaml", "templates/*/workflow.yaml", "modules/*/module.yaml", ".wfx/workflows/*.yaml"} {
		files, _ := filepath.Glob(filepath.Join(root, glob))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			for _, run := range runBlocks(string(b)) {
				n++
				if err := checkRunText(run, ""); err != nil {
					t.Errorf("%s: %v\n%s", f, err, run)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no run steps to check")
	}
}

// runBlocks pulls every `run:` value out of a YAML file without the loader
// (a module or a template is not a loadable workflow on its own).
func runBlocks(doc string) []string {
	var out []string
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		k := strings.Index(l, "run:")
		if k < 0 || strings.TrimSpace(l[:k]) != "" && strings.TrimSpace(l[:k]) != "-" {
			continue
		}
		rest := strings.TrimSpace(l[k+4:])
		if rest != "|" && rest != "|-" && rest != ">" {
			if rest != "" {
				out = append(out, strings.Trim(rest, `'`))
			}
			continue
		}
		ind := -1
		var body []string
		for i++; i < len(lines); i++ {
			s := lines[i]
			if strings.TrimSpace(s) == "" {
				body = append(body, "")
				continue
			}
			w := len(s) - len(strings.TrimLeft(s, " "))
			if ind < 0 {
				ind = w
			}
			if w < ind {
				i--
				break
			}
			body = append(body, s[ind:])
		}
		out = append(out, strings.Join(body, "\n"))
	}
	return out
}
