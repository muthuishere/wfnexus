package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A job's values reach the command on this machine byte for byte, through the
// environment, whatever quotes, newlines, delimiter lines or shell syntax they
// hold — and nothing in them runs.
func TestAJobsValuesArriveUnchangedOnTheWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	value := "a \"b\" 'c'\nWFX_V\n$(touch " + marker + ") `touch " + marker + "` ${HOME}\n\"; touch " + marker + "; echo \""
	big := strings.Repeat("y", 70<<10) + "\n$(touch " + marker + ")"
	rt, err := workflow.RenderRun(`printf '%s' "{{ .Input.v }}" > one
cat > two <<WFX_V
{{ .Input.v }}
WFX_V
printf '%s' "{{ .Input.big }}" > three`, workflow.TemplateData{Input: map[string]any{"v": value, "big": big}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rt.Script, "touch") {
		t.Fatalf("a value is in the command text: %q", rt.Script)
	}
	cfg := &Config{WorkDir: t.TempDir(), Name: "t", URL: "http://127.0.0.1:1"}
	res := do(context.Background(), cfg, payload{RunID: "r1", StepID: "s", Command: rt.Script, Data: rt.Data, Shell: "sh"})
	if res.Error != "" || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	dir := filepath.Join(cfg.WorkDir, "runs", "r1")
	for name, want := range map[string]string{"one": value, "two": value + "\n", "three": strings.TrimRight(big, "\n")} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %d bytes, want %d (%v)", name, len(got), len(want), err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("part of a value ran as a command")
	}
}
