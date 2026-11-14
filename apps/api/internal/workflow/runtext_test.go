package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// filled is a run text as the script receives it once its values are in
// place: what a test about substitution compares.
func filled(t *testing.T, run string, input map[string]any) string {
	t.Helper()
	return filledStep(t, Step{Run: run}, input)
}

// filledStep is filled for a loaded step, with its own constants.
func filledStep(t *testing.T, s Step, input map[string]any) string {
	t.Helper()
	rt, err := RenderRun(s.Run, TemplateData{Input: input, Const: s.RunConst})
	if err != nil {
		t.Fatal(err)
	}
	return rt.replace(func(i int) string { return rt.Data[i] })
}

const awkward = "a \"b\" 'c'\n$(touch x) `id` ${HOME} \\ \nWFX_V\nend"

func TestRunValuesTravelAsDataNotText(t *testing.T) {
	rt, err := RenderRun(`echo "{{ .Input.who }}" {{ .Steps.a.out }} {{ .Input.list }}`,
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
	rt, err := RenderRun(`{{ .Input.v }}{{ .Steps.s.v }}{{ .Output.v }}{{ .Decide.v }}{{ .Item }}{{ .Step.v }}{{ .Workflow.v }}{{ .Project.v }}{{ .Global.v }}{{ .Memory }}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"I", "S", "O", "D", "IT", "ST", "W", "P", "G", "M"}; strings.Join(rt.Data, ",") != strings.Join(want, ",") {
		t.Fatalf("data = %q", rt.Data)
	}
	if strings.Trim(rt.Display(), "${}WFX_DATA_0123456789") != "" {
		t.Fatalf("script still holds text: %q", rt.Display())
	}
}

// The run's own facts are data like every other value, and a missing value
// is empty.
func TestTheRunsOwnFactsAreDataToo(t *testing.T) {
	rt, err := RenderRun(`cd "{{ .WorkDir }}" && git log "{{ .BaseRef }}" && echo "{{ .RunID }}" "{{ .Index }}"`,
		TemplateData{WorkDir: "/w", BaseRef: "main", RunID: "r1", Index: 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rt.Data, ",") != "/w,main,r1,2" || rt.Display() != `cd "${WFX_DATA_1}" && git log "${WFX_DATA_2}" && echo "${WFX_DATA_3}" "${WFX_DATA_4}"` {
		t.Fatalf("script %q data %q", rt.Display(), rt.Data)
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
	rt, _ := RenderRun(`"{{ .Input.pr_number }}" "{{ .Input.PR_NUMBER }}" "{{ .Input.PrNumber }}"`,
		TemplateData{Input: map[string]any{"pr_number": "1", "PR_NUMBER": "2", "PrNumber": "3"}})
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
		{`echo {{ .Input.x }}`, "", "bare word"},
		{`gh pr view {{ .Input.n }}`, "", "bare word"},
		{"echo `{{ .Input.x }}`", "", "bare word"},
		{`x={{ .Input.x }}; echo "$x"`, "", "bare word"},
		{"cat > f <<EOF\n{{ .Input.x }}\nEOF", "", ""},
		{`echo '{{ .WorkDir }}'`, "", "single quotes"},
		{`cd "{{ .WorkDir }}" && git log "{{ .BaseRef }}..HEAD"`, "", ""},
		{`echo "{{ .RunID }}" "{{ .Index }}"`, "", ""},
		{`echo {{ .RunID }}`, "", "bare word"},
		{`echo '{{ .Input.x }}'`, "", "single quotes"},
		{`echo "$(echo '{{ .Input.x }}')"`, "", "single quotes"},
		{"cat > f <<'EOF'\n{{ .Input.x }}\nEOF", "", "quoted heredoc"},
		{"cat > f <<-\"EOF\"\n\t{{ .Input.x }}\n\tEOF\necho done", "", "quoted heredoc"},
		{"cat > f <<'EOF'\nstatic\nEOF\necho \"{{ .Input.x }}\"", "", ""},
		{"# it's a comment\necho \"{{ .Input.x }}\"", "", ""},
		{`echo 'a' "{{ .Input.x }}" 'b'`, "", ""},
		{`{{ if .Input.x }}'flag'{{ end }}`, "", "is not allowed"},
		{`echo {{ .Input.x }}`, "cmd", "cmd step"},
		{`echo '{{ .Input.x }}'`, "pwsh", "single quotes"},
		// named templates are separate trees: refused
		{`{{ define "t" }}echo "{{ .Input.x }}"{{ end }}{{ template "t" . }}`, "", "is not allowed"},
		{`{{ block "b" . }}echo "{{ .Input.x }}"{{ end }}`, "", "is not allowed"},
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
		// a platform fact's name under a dot moved onto a value is that value's
		{`{{ with .Item }}echo {{ .RunID }}{{ end }}`, "", "is not allowed"},
		{`{{ range .Input.objs }}echo {{ .Index }}; {{ end }}`, "", "is not allowed"},
		{`{{ with .Input.o }}echo "{{ .RunID }}"{{ end }}`, "", "is not allowed"},
		{`{{ with .Input.o }}echo {{ $.RunID }}{{ end }}`, "", "is not allowed"},
		{`{{ with .Input.o }}x{{ else }}echo {{ .RunID }}{{ end }}`, "", "is not allowed"},
		// trap actions, integer variables, quoted or escaped command words
		{`trap "echo {{ .Input.x }}" EXIT`, "", "trap"},
		{`trap 'echo "$v"' EXIT; v="{{ .Input.x }}"`, "", ""},
		{`declare -i n="{{ .Input.x }}"`, "", "integer variable"},
		{`typeset -i n="{{ .Input.x }}"`, "", "integer variable"},
		{`f() { local -ri n="{{ .Input.x }}"; }`, "", "integer variable"},
		{`declare n="{{ .Input.x }}"`, "", ""},
		{`"sh" -c "echo {{ .Input.x }}"`, "", "sh -c"},
		{`'bash' -c "echo {{ .Input.x }}"`, "", "bash -c"},
		{`\sh -c "echo {{ .Input.x }}"`, "", "sh -c"},
		{`\eval "echo {{ .Input.x }}"`, "", "eval"},
		{`"eval" "echo {{ .Input.x }}"`, "", "eval"},
		{`sh -c"echo {{ .Input.x }}"`, "", "sh -c"},
		{`su deploy -c "echo {{ .Input.x }}"`, "", "su -c"},
		{`su - deploy -c "echo {{ .Input.x }}"`, "", "su -c"},
		{`Invoke-Expression "echo {{ .Input.x }}"`, "pwsh", "Invoke-Expression"},
		{`iex "echo {{ .Input.x }}"`, "pwsh", "Invoke-Expression"},
		// text fed to a shell or interpreter as its script
		{"bash <<EOF\necho \"{{ .Input.x }}\"\nEOF", "", "heredoc that bash reads"},
		{"sh -s <<EOF\necho {{ .Input.x }}\nEOF", "", "heredoc that sh reads"},
		{"python3 - <<PY\nprint(\"{{ .Input.x }}\")\nPY", "", "heredoc that python3 reads"},
		{"cat <<EOF | sh\necho {{ .Input.x }}\nEOF", "", "heredoc that sh reads"},
		{"node <<JS\nconsole.log(\"{{ .Input.x }}\")\nJS", "", "heredoc that node reads"},
		{`echo "{{ .Input.x }}" | sh`, "", "piped into sh"},
		{`printf '%s' "{{ .Input.x }}" | tr a b | bash -s`, "", "piped into bash"},
		{`echo "{{ .Input.x }}" | env X=1 zsh`, "", "piped into zsh"},
		{`"{{ .Input.x }}" | iex`, "pwsh", "piped into iex"},
		{`bash <<<"{{ .Input.x }}"`, "", "here-string"},
		{`source <(echo "{{ .Input.x }}")`, "", "here-string or <( … )"},
		// the same readers given values as data
		{"cat > f <<EOF\n{{ .Input.x }}\nEOF\nbash f.sh", "", ""},
		{`echo "{{ .Input.x }}" | grep -c sh`, "", ""},
		{`echo "{{ .Input.x }}" || sh -c 'exit 1'`, "", ""},
		{`V="{{ .Input.x }}" bash <<'EOF'` + "\necho \"$V\"\nEOF", "", ""},
		// the same words where nothing is evaluated again
		{`sh -c 'echo "$1"' _ "{{ .Input.x }}"`, "", ""},
		{`[[ "{{ .Input.x }}" == yes ]] && echo y`, "", ""},
		{`echo "eval {{ .Input.x }}"`, "", ""},
		{`echo "sh -c {{ .Input.x }}"`, "", ""},
		{`n=$(( 1 + 2 )); echo "{{ .Input.x }}"`, "", ""},
		{`grep "a[{{ .Input.x }}]" f`, "", ""},
		{`cd {{ .WorkDir }} && echo "{{ .Input.x }}"`, "", "bare word"},
		{`git log {{ .BaseRef }}..HEAD`, "", "bare word"},
		// $ is never declared or assigned, so $.RunID is always the run's
		{`{{ $ = .Input.o }}echo "{{ $.RunID }}"`, "", "is not allowed"},
		{`{{ $ := .Input.o }}echo "{{ $.RunID }}"`, "", "is not allowed"},
		{`{{ range $ := .Input.objs }}echo "{{ $.RunID }}"{{ end }}`, "", "is not allowed"},
		{`{{ range $i, $ := .Input.objs }}echo "{{ $.RunID }}"{{ end }}`, "", "is not allowed"},
		{`{{ with $ := .Input.o }}echo "{{ $.RunID }}"{{ end }}`, "", "is not allowed"},
		{`{{ if $ = .Input.o }}x{{ end }}echo "{{ $.RunID }}"`, "", "is not allowed"},
		{`{{ range .Input.objs }}{{ $ = . }}{{ end }}echo "{{ $.RunID }}"`, "", "is not allowed"},
		{`{{ if .Input.x }}{{ with .Input.o }}{{ $ = . }}{{ end }}{{ end }}echo {{ $.RunID }}`, "", "is not allowed"},
		{`{{ $o := .Input.o }}{{ range $i, $v := .Input.objs }}echo "{{ $v }}" {{ $.RunID }}{{ end }}`, "", "is not allowed"},
		// more ways to hand a value to a shell as its script
		{`bash -o pipefail -c "echo {{ .Input.x }}"`, "", "bash -c"},
		{`bash --norc -o errexit -c "echo {{ .Input.x }}"`, "", "bash -c"},
		{`bash -c -- "echo {{ .Input.x }}"`, "", "bash -c"},
		{`tcsh -c "echo {{ .Input.x }}"`, "", "tcsh -c"},
		{`csh -c "echo {{ .Input.x }}"`, "", "csh -c"},
		{`"$BASH" -c "echo {{ .Input.x }}"`, "", "-c"},
		{`$SHELL -c "echo {{ .Input.x }}"`, "", "-c"},
		{`echo "{{ .Input.x }}" | "$BASH"`, "", "piped into $bash"},
		{`echo "{{ .Input.x }}" |& bash`, "", "piped into bash"},
		{"echo \"{{ .Input.x }}\" |\n  bash", "", "piped into bash"},
		{"echo \"{{ .Input.x }}\" | \\\n  bash", "", "piped into bash"},
		{`x=$(echo "{{ .Input.x }}" | bash)`, "", "piped into bash"},
		{`echo "{{ .Input.x }}" | env -u X bash`, "", "piped into bash"},
		{`echo "{{ .Input.x }}" | sudo -u root bash`, "", "piped into bash"},
		{`echo "{{ .Input.x }}" | xargs -I {} sh`, "", "piped into sh"},
		{`. /dev/stdin <<< "{{ .Input.x }}"`, "", "here-string"},
		{"bash \\\n  <<EOF\necho \"{{ .Input.x }}\"\nEOF", "", "heredoc that bash reads"},
		{`python3 -c "print('{{ .Input.x }}')"`, "", "python3 -c"},
		{`python3 -c "print({{ .Input.x }})"`, "", "python3 -c"},
		{`python -u -c "import os; print(\"{{ .Input.x }}\")"`, "", "python -c"},
		{`node -e "console.log(\"{{ .Input.x }}\")"`, "", "node -e"},
		{`perl -e "print \"{{ .Input.x }}\""`, "", "perl -e"},
		{`ruby -e "puts \"{{ .Input.x }}\""`, "", "ruby -e"},
		{`pwsh -Command "Write-Output \"{{ .Input.x }}\""`, "", "pwsh -Command"},
		{`pwsh -NoProfile -Command Write-Output "{{ .Input.x }}"`, "", "pwsh -Command"},
		// the same programs given the value as data
		{`python3 -c 'import sys; print(sys.argv[1])' "{{ .Input.x }}"`, "", ""},
		{`node -e 'console.log(process.argv[1])' "{{ .Input.x }}"`, "", ""},
		{`perl -e 'print $ARGV[0]' "{{ .Input.x }}"`, "", ""},
		{`bash -o pipefail -c 'echo "$1"' _ "{{ .Input.x }}"`, "", ""},
		{`bash -c -- 'echo "$1"' _ "{{ .Input.x }}"`, "", ""},
		{`echo "{{ .Input.x }}" | env -u X grep -c y`, "", ""},
		{`python3 script.py "{{ .Input.x }}"`, "", ""},
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

// Every workflow, template, module and example shipped in this repository
// passes the check, so none of them is refused after this change.
func TestShippedRunTextPassesTheCheck(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	n := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || (strings.HasPrefix(name, ".") && name != "." && name != ".." && name != ".wfx") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
			return nil
		}
		b, _ := os.ReadFile(p)
		var doc any
		if yaml.Unmarshal(b, &doc) != nil {
			return nil
		}
		for _, r := range runValues(doc) {
			n++
			if err := checkRunText(r.run, r.shell); err != nil {
				t.Errorf("%s: %v\n%s", p, err, r.run)
			}
		}
		return nil
	})
	t.Logf("%d run blocks, all load", n)
	if n == 0 {
		t.Fatal("found no run steps to check")
	}
}

type runValue struct{ run, shell string }

// runValues is every `run:` string anywhere in a YAML document, with the
// `shell:` beside it.
func runValues(v any) []runValue {
	var out []runValue
	switch x := v.(type) {
	case map[string]any:
		if r, ok := x["run"].(string); ok {
			sh, _ := x["shell"].(string)
			out = append(out, runValue{r, sh})
		}
		for _, c := range x {
			out = append(out, runValues(c)...)
		}
	case []any:
		for _, c := range x {
			out = append(out, runValues(c)...)
		}
	}
	return out
}

// `$` may not be declared or assigned in run text: $.RunID is printed as text
// only because `$` always holds the run's own data. Assigning a caller's
// object to `$` used to put that object's RunID field into the script.
func TestDollarIsNeverReassigned(t *testing.T) {
	evil := "x\"; touch /tmp/p; echo \""
	data := TemplateData{RunID: "r1", Input: map[string]any{
		"o":    map[string]any{"RunID": evil},
		"objs": []any{map[string]any{"RunID": evil}},
	}}
	for _, run := range []string{
		`{{ $ = .Input.o }}echo "{{ $.RunID }}"`,
		`{{ range $ := .Input.objs }}echo "{{ $.RunID }}"{{ end }}`,
		`{{ range $i, $ := .Input.objs }}echo "{{ $.RunID }}"{{ end }}`,
		`{{ with $ := .Input.o }}echo "{{ $.RunID }}"{{ end }}`,
		`{{ $ := .Input.o }}echo "{{ $.RunID }}"`,
	} {
		rt, err := RenderRun(run, data)
		if err == nil {
			t.Errorf("%s rendered: %q", run, rt.Script)
			continue
		}
		if strings.Contains(rt.Script, "touch") {
			t.Errorf("%s: the value reached the script: %q", run, rt.Script)
		}
	}
}
