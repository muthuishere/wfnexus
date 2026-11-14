package workflow

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// plainRef is the test's own statement of what run text may hold: one field
// path on the root dot, with optional trim markers (a step id or state key
// may hold a hyphen).
var plainRef = regexp.MustCompile(`^\{\{(?:- )?\s*(?:\.[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*|\.(?:Steps|Step|Workflow|Project|Global)(?:\.[A-Za-z0-9_-]+)+)\s*(?: -)?\}\}$`)

// actions are the pieces the generator puts inside {{ … }}.
var fuzzPieces = []string{
	".Input.x", ".Input.o.RunID", ".Steps.a.output.y", ".Item.z", ".RunID", ".Index", ".WorkDir", ".BaseRef",
	".Workflow.k", ".Memory", ".Decide.q", ".Output.v", ".", "$", "$.RunID", "$x", "$x := .Input.o", "$ = .Input",
	"$ := .Input", "if .Input.x", "else", "end", "range .Input.l", "range $i, $v := .Input.l", "with .Input.o",
	"define \"t\"", "template \"t\" .", "block \"b\" .", "break", "continue", "printf \"%s\" .Input.x",
	"len .Input.x", "index .Input \"x\"", "eq .Input.x \"y\"", "json .Input", "or .Input.x \"\"",
	"(.Input.x)", "(.Input).x", ".Input.x | printf \"%s\"", "\"lit\"", "1", "true", "nil", ".Input.x.Method 1",
	"/* a comment */", "($ = .Input)", "len ($ = .Input)", "$y := ($ = .Input)", "or ($ = .Input) 1", "",
	".Steps.my-step.output", ".Input.my-key", "call .Input.f", "html .Input.x", ".Input.x \"arg\"",
}

func fuzzAction(r *rand.Rand) string {
	open, close := "{{ ", " }}"
	if r.Intn(4) == 0 {
		open = "{{- "
	}
	if r.Intn(4) == 0 {
		close = " -}}"
	}
	if r.Intn(6) == 0 {
		open, close = "{{", "}}"
	}
	return open + fuzzPieces[r.Intn(len(fuzzPieces))] + close
}

// Generated run text, mixing every kind of template construct, is either all
// plain references — each rendered as an environment reference, none of its
// value in the script — or refused. Never anything else.
func TestRunTextAllowsOnlyPlainReferences(t *testing.T) {
	const inject = "INJECT$(touch /tmp/p)\"'`"
	data := TemplateData{RunID: inject, WorkDir: inject, BaseRef: inject, Memory: inject, Index: 7,
		Input:    map[string]any{"x": inject, "o": map[string]any{"RunID": inject}, "l": []any{inject}},
		Steps:    map[string]any{"a": map[string]any{"output": map[string]any{"y": inject}}},
		Item:     map[string]any{"z": inject},
		Workflow: map[string]string{"k": inject}, Decide: map[string]any{"q": inject}, Output: map[string]any{"v": inject},
	}
	r := rand.New(rand.NewSource(136))
	accepted, refused := 0, 0
	for n := 0; n < 5000; n++ {
		var b strings.Builder
		var actions []string
		for k := 0; k < 1+r.Intn(4); k++ {
			b.WriteString([]string{`echo "`, `x="`, "cat <<EOF\n", `printf '%s' "`}[r.Intn(4)])
			a := fuzzAction(r)
			if r.Intn(5) == 0 { // nesting
				a += fuzzAction(r) + fuzzAction(r)
			}
			actions = append(actions, regexp.MustCompile(`\{\{.*?\}\}`).FindAllString(a, -1)...)
			b.WriteString(a)
			b.WriteString([]string{"\"\n", "\"; ", "\nEOF\n", "\" > f\n"}[r.Intn(4)])
		}
		text := b.String()
		err := checkRunText(text, "sh")
		rt, rerr := RenderRun(text, data)
		allPlain := true
		refs := 0
		for _, a := range actions {
			if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(a, "{{"), "- "), "/*") {
				continue
			}
			if !plainRef.MatchString(a) {
				allPlain = false
			}
			refs++
		}
		if err == nil {
			accepted++
			if !allPlain {
				t.Fatalf("accepted a construct that is not a plain reference:\n%s", text)
			}
			if rerr != nil {
				t.Fatalf("accepted at load but did not render: %v\n%s", rerr, text)
			}
			if strings.Contains(rt.Script, "INJECT") || strings.Contains(rt.Script, "{{") || len(rt.Data) != refs {
				t.Fatalf("rendered %q with %d values for %d references\n%s", rt.Script, len(rt.Data), refs, text)
			}
			continue
		}
		refused++
		if !allPlain && rerr == nil {
			t.Fatalf("refused at load but rendered anyway:\n%s", text)
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the generator did not cover both outcomes: %d accepted, %d refused", accepted, refused)
	}
}

// `$` reassigned inside parentheses, and the shapes that used it, are refused
// like every other construct.
func TestDollarInParenthesesIsRefused(t *testing.T) {
	for _, run := range []string{
		`echo "{{ if ($ = .Input) }}{{ end }}{{ $.RunID }}"`,
		`echo "{{ $x := ($ := .Input) }}{{ $.RunID }}"`,
		`echo "{{ len ($ = .Input) }}{{ $.RunID }}"`,
		`echo "{{ with $y := ($ = .Input) }}{{ end }}{{ $.RunID }}"`,
		`echo "{{ $z := (or ($ = .Input) 1) }}{{ $.RunID }}"`,
		`echo "run {{ if ($ = .Input) }}{{ end }}{{ $.RunID }}"`,
	} {
		if err := checkRunText(run, "sh"); err == nil || !strings.Contains(err.Error(), "is not allowed") {
			t.Errorf("%s: %v", run, err)
		}
		if _, err := RenderRun(run, TemplateData{Input: map[string]any{"RunID": `"; echo INJECTED-$((6*7)); echo "`}}); err == nil {
			t.Errorf("%s rendered", run)
		}
	}
}

// Chains of fields are references; a key that only index could reach is
// refused with a pointer to reading the whole value as JSON.
func TestKeysThatNeedIndexAreRefusedWithAWayOut(t *testing.T) {
	for _, run := range []string{`echo "{{ .Steps.x.output.y }}"`, `echo "{{ .Item.z }}"`, `echo "{{ .Steps.my-step.output }}"`} {
		if err := checkRunText(run, "sh"); err != nil {
			t.Errorf("%s refused: %v", run, err)
		}
	}
	for _, run := range []string{`echo "{{ index .Steps.x.output "odd key" }}"`, `echo "{{ .Input.my-key }}"`} {
		err := checkRunText(run, "sh")
		if err == nil || !strings.Contains(err.Error(), "jq") {
			t.Errorf("%s: %v", run, err)
		}
	}
	rt, err := RenderRun(`printf '%s' "{{ .Steps.x.output }}" | jq -r '.["odd key"]'`,
		TemplateData{Steps: map[string]any{"x": map[string]any{"output": map[string]any{"odd key": "v"}}}})
	if err != nil || !strings.Contains(rt.Data[0], `"odd key": "v"`) {
		t.Fatalf("data %q %v", rt.Data, err)
	}
}

// runSh runs rendered run text under sh and returns its stdout.
func runSh(t *testing.T, run string, data TemplateData) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	rt, err := RenderRun(run, data)
	if err != nil {
		t.Fatal(err)
	}
	script, env, err := rt.For("sh", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return string(out)
}

// Where a reference may sit, and what it becomes there.
func TestAReferenceInEachQuotingContext(t *testing.T) {
	v := `a "b" 'c' $HOME $(echo x) ` + "`echo y`"
	data := TemplateData{Input: map[string]any{"x": v}}
	// refused: a silent literal ${WFX_…} would be wrong
	for _, run := range []string{
		`echo '{{ .Input.x }}'`,
		"cat <<'EOF'\n{{ .Input.x }}\nEOF",
		`echo {{ .Input.x }}`,
	} {
		for _, sh := range []string{"sh", "pwsh"} {
			if run[0] == 'c' && sh == "pwsh" {
				continue // heredocs are POSIX
			}
			if err := checkRunText(run, sh); err == nil {
				t.Errorf("%s (%s) accepted", run, sh)
			}
		}
	}
	// inside a double-quoted string: expands once, the string stays whole
	if got := runSh(t, `printf '%s|' "hello {{ .Input.x }} bye"`, data); got != "hello "+v+" bye|" {
		t.Errorf("double quotes: %q", got)
	}
	// in an unquoted heredoc: expands once
	if got := runSh(t, "cat <<EOF\n<{{ .Input.x }}>\nEOF", data); got != "<"+v+">\n" {
		t.Errorf("heredoc: %q", got)
	}
	// pwsh: the same double-quoted string refers to the variable its own way
	rt, _ := RenderRun(`Write-Output "hello {{ .Input.x }} bye"`, data)
	script, env, _ := rt.For("pwsh", "")
	if script != `Write-Output "hello ${env:WFX_DATA_1} bye"` || env[0] != "WFX_DATA_1="+v {
		t.Errorf("pwsh: %q %q", script, env)
	}
	if err := checkRunText(`Write-Output "hello {{ .Input.x }} bye"`, "pwsh"); err != nil {
		t.Errorf("pwsh double quotes refused: %v", err)
	}
}

// Text fed to a program on stdin is code only when the program has no script
// file and no code argument, or the argument is `-`.
func TestStdinIsDataWhenTheProgramHasItsOwnCode(t *testing.T) {
	for _, run := range []string{
		`echo "{{ .Input.x }}" | python3 -c 'import json,sys; print(json.load(sys.stdin))'`,
		`echo "{{ .Input.x }}" | node -e 'process.stdin.pipe(process.stdout)'`,
		`echo "{{ .Input.x }}" | perl -ne 'print'`,
		`echo "{{ .Input.x }}" | python3 parse.py`,
		`echo "{{ .Input.x }}" | bash ./consume.sh`,
		`echo "{{ .Input.x }}" | ssh host 'cat > f'`,
		"python3 analyze.py <<EOF\n{{ .Input.x }}\nEOF",
		`ruby -e 'puts STDIN.read' <<< "{{ .Input.x }}"`,
	} {
		if err := checkRunText(run, "bash"); err != nil {
			t.Errorf("%q refused: %v", run, err)
		}
	}
	for _, run := range []string{
		`echo "{{ .Input.x }}" | python3`,
		`echo "{{ .Input.x }}" | python3 -`,
		`echo "{{ .Input.x }}" | bash -s`,
		`echo "{{ .Input.x }}" | ssh host`,
		`echo "{{ .Input.x }}" | perl`,
		"python3 - <<EOF\n{{ .Input.x }}\nEOF",
		`bash -s -- arg <<< "{{ .Input.x }}"`,
	} {
		if err := checkRunText(run, "bash"); err == nil {
			t.Errorf("%q accepted", run)
		}
	}
}

// A prompt keeps full templates. Whatever an agent step's prompt rendered,
// and whatever the agent answered, reaches a later run step as data only.
func TestPromptTextReachesRunStepsOnlyAsData(t *testing.T) {
	prompt, err := Render(`Summarise {{ .Input.topic }}; {{ if .Input.topic }}with care{{ end }}`,
		TemplateData{Input: map[string]any{"topic": `{{ .RunID }} "; touch /tmp/p; echo "`}})
	if err != nil || !strings.Contains(prompt, "with care") {
		t.Fatalf("prompt %q %v", prompt, err)
	}
	answer := map[string]any{"text": prompt + " $(touch /tmp/q)"}
	rt, err := RenderRun(`printf '%s' "{{ .Steps.agent.output.text }}" > out`,
		TemplateData{RunID: "r1", Steps: map[string]any{"agent": map[string]any{"output": answer}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rt.Script, "touch") || len(rt.Data) != 1 || rt.Data[0] != answer["text"] {
		t.Fatalf("script %q data %q", rt.Script, rt.Data)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if runtime.GOOS != "windows" {
		script, env, _ := rt.For("sh", dir)
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(out); string(b) != answer["text"] {
			t.Fatalf("out = %q", b)
		}
	}
}

// The shapes shipped workflows moved to keep their behaviour: the template's
// fallback (if/else, default) now lives in the script, and objects that were
// written with json arrive as the same JSON.
func TestMigratedFallbacksBehaveAsBefore(t *testing.T) {
	brief := "day=\"{{ .Input.day }}\"\nprintf '%s' \"Daily brief ${day:-fallback}\""
	since := `since="{{ .Input.since }}"; last_sha="{{ .Workflow.last_sha }}"; printf '%s' "${since:-$last_sha}"`
	publish := "publish=\"{{ .Steps.publish }}\"; pr_url=\"{{ .Steps.publish.pr_url }}\"\n[ -n \"$publish\" ] && printf '%s' \"PR: $pr_url\"; true"
	for _, c := range []struct {
		run  string
		data TemplateData
		want string
	}{
		{brief, TemplateData{Input: map[string]any{"day": "2026-11-14"}}, "Daily brief 2026-11-14"},
		{brief, TemplateData{Input: map[string]any{}}, "Daily brief fallback"},
		{since, TemplateData{Input: map[string]any{"since": "abc"}, Workflow: map[string]string{"last_sha": "def"}}, "abc"},
		{since, TemplateData{Input: map[string]any{}, Workflow: map[string]string{"last_sha": "def"}}, "def"},
		{`since="{{ .Workflow.last_id }}"; since="${since:-0}"; printf '%s' "$since"`, TemplateData{}, "0"},
		{`api="{{ .Input.api }}"; printf '%s' "${api:-http://127.0.0.1:8090}"`, TemplateData{Input: map[string]any{}}, "http://127.0.0.1:8090"},
		{"jev=\"{{ .Steps.jev }}\"\ncat <<EOF\n{\"jev\": ${jev:-null}}\nEOF", TemplateData{Steps: map[string]any{}}, "{\"jev\": null}\n"},
		{"cat <<EOF\n{{ .Steps.jev }}\nEOF", TemplateData{Steps: map[string]any{"jev": map[string]any{"pass": true}}}, "{\n  \"pass\": true\n}\n"},
		{publish, TemplateData{Steps: map[string]any{}}, ""},
		{publish, TemplateData{Steps: map[string]any{"publish": map[string]any{"pr_url": "u"}}}, "PR: u"},
	} {
		if err := checkRunText(c.run, "sh"); err != nil {
			t.Errorf("%q refused: %v", c.run, err)
			continue
		}
		if got := runSh(t, c.run, c.data); got != c.want {
			t.Errorf("%q: got %q, want %q", c.run, got, c.want)
		}
	}
}
