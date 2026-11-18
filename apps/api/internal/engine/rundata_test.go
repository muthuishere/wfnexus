package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A step placed on a worker is sent its command WITHOUT the values, and the
// values beside it; the worker's shell then reads them from the environment
// and gets them byte for byte.
func TestAWorkerIsSentValuesBesideTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	label := "box-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "placed-data", Steps: []workflow.Step{{
		ID: "echo", Run: `printf '%s' "{{ .Input.v }}"`, RunsOn: label,
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	tok, err := h.eng.RegistrationToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined, err := h.eng.Join(context.Background(), JoinRequest{Token: tok, Name: "dbox-" + time.Now().Format("150405.000"), Labels: []string{label}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.store.DeleteWorker(context.Background(), joined.Worker.ID) })

	marker := filepath.Join(t.TempDir(), "ran")
	value := "x\"; touch " + marker + "; echo \"\nWFX_V\n`id` $(id) end"
	sent := make(chan *JobPayload, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		w, err := h.eng.AuthWorker(ctx, joined.Token)
		if err != nil {
			return
		}
		job, err := h.eng.Claim(ctx, w, 20*time.Second)
		if err != nil || job == nil {
			return
		}
		var p JobPayload
		_ = json.Unmarshal(job.Payload, &p)
		// What wfx-runner does with a job (cmd/wfx-runner do()).
		script, env, _ := workflow.RunText{Script: p.Command, Data: p.Data}.For("sh", t.TempDir())
		c := exec.Command("sh", "-c", script)
		c.Env = append(os.Environ(), env...)
		out, err := c.Output()
		code := 0
		if err != nil {
			code = 1
		}
		res, _ := json.Marshal(JobResult{OK: code == 0, ExitCode: code, Stdout: string(out)})
		_ = h.store.FinishJob(ctx, job.ID, w.ID, res)
		sent <- &p
	}()

	run := h.run(map[string]any{"v": value})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	p := <-sent
	if strings.Contains(p.Command, "touch") || len(p.Data) != 1 || p.Data[0] != value {
		t.Fatalf("payload command %q data %q", p.Command, p.Data)
	}
	steps, _ := h.store.ListSteps(context.Background(), run.ID)
	var out map[string]any
	_ = json.Unmarshal(steps[0].Output, &out)
	if out["stdout"] != value {
		t.Fatalf("stdout = %q", out["stdout"])
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("part of the value ran")
	}
	// The run page shows a reference, never the value.
	if strings.Contains(steps[0].Prompt, "touch") {
		t.Fatalf("recorded command holds the value: %q", steps[0].Prompt)
	}
}
