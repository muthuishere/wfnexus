package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func dataServer(t *testing.T, files map[string]string) exposeFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(wf, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(),
		SecretKeyEnv: "WFX_DATA_TEST_NO_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "k")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	return exposeFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}
}

func (f exposeFixture) runToEnd(t *testing.T, workflow string, input map[string]any) *store.Run {
	t.Helper()
	b, _ := json.Marshal(input)
	code, out, _ := f.call(t, "POST", "/api/workflows/"+workflow+"/runs", b, nil)
	if code != 201 {
		t.Fatalf("start %s = %d %v", workflow, code, out)
	}
	id := uuid.MustParse(out["run"].(map[string]any)["id"].(string))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r, err := f.st.GetRun(context.Background(), id)
		if err == nil && (r.Status == "done" || r.Status == "failed") {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
	return nil
}

// The two ways a run step commonly writes a value — inside double quotes and
// in a heredoc — each fed a value holding quotes, a line equal to the heredoc
// delimiter, newlines, backticks and $( ). The value must arrive byte for byte
// and nothing in it may run.
func TestRunStepValuesArriveUnchangedAndNothingElseRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	f := dataServer(t, map[string]string{
		"quoted.yaml": "name: quoted\ninput_schema: {type: object, properties: {v: {type: string}}}\nsteps:\n" +
			"  - id: s\n    shell: sh\n    run: printf '%s' \"{{ .Input.v }}\" > " + filepath.Join(dir, "quoted") + "\n",
		"heredoc.yaml": "name: heredoc\ninput_schema: {type: object, properties: {v: {type: string}}}\nsteps:\n" +
			"  - id: s\n    shell: sh\n    run: |\n      cat > " + filepath.Join(dir, "heredoc") + " <<WFX_V\n      {{ .Input.v }}\n      WFX_V\n",
	})
	values := map[string]string{
		// a double quote that closes the script's own quotes
		"quote": "x\"; touch " + marker + "; echo \"",
		// a line equal to the heredoc's delimiter
		"delimiter": "x\nWFX_V\ntouch " + marker + "\ncat > /dev/null <<WFX_V",
		// a leading '-': must stay data, never become an option
		"dash": "--help -rf /nonexistent",
		// newlines, backticks and $( )
		"mixed": "line1\n`touch " + marker + "`\n$(touch " + marker + ")\n${HOME} \\ 'single' \"double\"\n",
	}
	for name, v := range values {
		for wf, file := range map[string]string{"demo%2Fquoted": "quoted", "demo%2Fheredoc": "heredoc"} {
			t.Run(name+"/"+file, func(t *testing.T) {
				_ = os.Remove(marker)
				_ = os.Remove(filepath.Join(dir, file))
				r := f.runToEnd(t, wf, map[string]any{"v": v})
				if r.Status != "done" {
					t.Fatalf("run %s (%s)", r.Status, r.Error)
				}
				got, err := os.ReadFile(filepath.Join(dir, file))
				want := v
				if file == "heredoc" {
					want = v + "\n"
				}
				if err != nil || string(got) != want {
					t.Errorf("got %q, want %q (%v)", got, want, err)
				}
				if _, err := os.Stat(marker); err == nil {
					t.Errorf("part of the value ran as a command")
				}
			})
		}
	}
}

// Inside range or with the dot is a value, so a field of it named like one of
// the run's own facts (.RunID, .Index, …) is that value's and travels as
// data: nothing in it runs, and as a bare word it is refused at load.
func TestFactNamesOnACallersValueRunNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	evil := "x\"; touch " + marker + "; echo \""
	schema := "input_schema: {type: object, properties: {o: {type: object}, objs: {type: array}}}\n"
	f := dataServer(t, map[string]string{
		"withfact.yaml": "name: withfact\n" + schema + "steps:\n  - id: s\n    shell: sh\n    run: " +
			`'{{ with .Input.o }}printf "%s" "{{ .RunID }}" > ` + filepath.Join(dir, "with") + `{{ end }}'` + "\n",
		"rangefact.yaml": "name: rangefact\n" + schema + "steps:\n  - id: s\n    shell: sh\n    run: " +
			`'{{ range .Input.objs }}printf "%s" "{{ .Index }}" > ` + filepath.Join(dir, "range") + `; {{ end }}'` + "\n",
		"itemfact.yaml": "name: itemfact\n" + schema + "steps:\n  - id: s\n    shell: sh\n    foreach: input.objs\n    run: " +
			`'{{ with .Item }}echo {{ .RunID }}{{ end }}'` + "\n",
	})
	for wf, file := range map[string]string{"demo%2Fwithfact": "with", "demo%2Frangefact": "range"} {
		t.Run(file, func(t *testing.T) {
			_ = os.Remove(marker)
			r := f.runToEnd(t, wf, map[string]any{"o": map[string]any{"RunID": evil}, "objs": []any{map[string]any{"Index": evil}}})
			if r.Status != "done" {
				t.Fatalf("run %s (%s)", r.Status, r.Error)
			}
			if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != evil {
				t.Errorf("got %q, want %q (%v)", got, evil, err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("part of the value ran as a command")
			}
		})
	}
	t.Run("item-bare", func(t *testing.T) {
		_ = os.Remove(marker)
		b, _ := json.Marshal(map[string]any{"objs": []any{map[string]any{"RunID": "x; touch " + marker}}})
		if code, out, _ := f.call(t, "POST", "/api/workflows/demo%2Fitemfact/runs", b, nil); code == 201 {
			id := uuid.MustParse(out["run"].(map[string]any)["id"].(string))
			for i := 0; i < 200; i++ {
				if r, _ := f.st.GetRun(context.Background(), id); r != nil && (r.Status == "done" || r.Status == "failed") {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Error("the workflow loaded and started")
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("part of the value ran as a command")
		}
	})
}

// Shapes where the shell would evaluate a value a second time, and named
// templates whose bodies are separate trees, are refused when the workflow
// loads: it cannot be started, and nothing runs.
func TestRunTextThatWouldEvaluateAValueAgainIsRefusedAtLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	shapes := map[string]string{
		"named":     `{{ define "t" }}echo "{{ .Input.v }}"{{ end }}{{ template "t" . }}`,
		"arith":     `echo $(( {{ .Input.v }} + 1 ))`,
		"arithcmd":  `(( {{ .Input.v }} > 1 )) && echo big`,
		"let":       `let n={{ .Input.v }}+1`,
		"numtest":   `[[ {{ .Input.v }} -eq 1 ]] && echo one`,
		"subscript": `a=(x y); echo "${a[{{ .Input.v }}]}"`,
		"shc":       `sh -c "echo {{ .Input.v }}"`,
		"bashc":     `bash -c "echo {{ .Input.v }}"`,
		"eval":      `eval "echo {{ .Input.v }}"`,
		"trap":      `trap "echo {{ .Input.v }}" EXIT`,
		"declarei":  `declare -i n="{{ .Input.v }}"`,
		"quotedsh":  `"sh" -c "echo {{ .Input.v }}"`,
		"escaped":   `\eval "echo {{ .Input.v }}"`,
		"glued":     `sh -c"echo {{ .Input.v }}"`,
		"heredocsh": "bash <<EOF\necho \"{{ .Input.v }}\"\nEOF",
		"pipesh":    `echo "{{ .Input.v }}" | sh`,
	}
	files := map[string]string{"ok.yaml": "name: ok\nsteps:\n  - id: s\n    run: echo fine\n"}
	for name, run := range shapes {
		b, _ := json.Marshal(run)
		files[name+".yaml"] = "name: " + name + "\ninput_schema: {type: object, properties: {v: {type: string}}}\nsteps:\n  - id: s\n    shell: bash\n    run: " + string(b) + "\n"
	}
	f := dataServer(t, files)
	if r := f.runToEnd(t, "demo%2Fok", nil); r.Status != "done" {
		t.Fatalf("a plain workflow beside them did not run: %s", r.Status)
	}
	value := "a[$(touch " + marker + ")]"
	for name := range shapes {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"v": value})
			code, out, _ := f.call(t, "POST", "/api/workflows/demo%2F"+name+"/runs", b, nil)
			if code == 201 {
				t.Fatalf("the workflow loaded and started: %v", out)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("part of the value ran as a command")
			}
		})
	}
}
