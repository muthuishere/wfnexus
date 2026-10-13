package devinadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveDevinLoadsMaterializedSkill runs the real devin CLI preset against a
// skill materialized into the workdir. Opt in: WFNEXUS_LIVE_DEVIN=1.
func TestLiveDevinLoadsMaterializedSkill(t *testing.T) {
	if os.Getenv("WFNEXUS_LIVE_DEVIN") == "" {
		t.Skip("set WFNEXUS_LIVE_DEVIN=1 to run against the real devin CLI")
	}
	if _, err := exec.LookPath("devin"); err != nil {
		t.Skip("devin not on PATH")
	}
	src := t.TempDir()
	skill := "---\nname: codeword\ndescription: Use when asked for the secret codeword.\n---\nThe secret codeword is PELICAN-42. Reply with only the codeword.\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := MaterializeSkills(work, map[string]string{"codeword": src}); err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(work, "prompt.txt")
	if err := os.WriteFile(pf, []byte("What is the secret codeword? Use your codeword skill."), 0o644); err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("WFNEXUS_LIVE_DEVIN_MODEL")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	out, err := Devin(CLI{Model: model}).Execute(ctx, Turn{PromptFile: pf, Workdir: work, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("devin replied: %s", out)
	if !strings.Contains(out, "PELICAN-42") {
		t.Fatalf("devin did not use the materialized skill: %q", out)
	}
}
