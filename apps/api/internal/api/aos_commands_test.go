package api

import (
	"strings"
	"testing"
)

// aos module commands through `wfx module call` against a stub aos. They run
// only on a worker (WFX_RUNNER, set by wfx-runner; "box" here is served in
// process to stand in for one). shell and file-write are write commands, and a
// write call may run only on the server's own runner for now, so they are not
// offered.
func TestAosCommandWrappers(t *testing.T) {
	h, eng, tmp := aplServer(t, nil)
	refused := map[string]bool{}
	for _, s := range eng.SourceSkips() {
		if strings.Contains(s.Reason, "write commands run on the server's runner only for now") {
			refused[s.Workflow] = true
			continue
		}
		t.Errorf("skipped: %s %s", s.Workflow, s.Reason)
	}
	if !refused["aos.shell"] || !refused["aos.file-write"] {
		t.Fatalf("aos write commands loaded on a worker: %v", refused)
	}
	calls := stubCLI(t, tmp, "aos", "AOS_BIN")

	// Not on a worker: refused, aos never runs.
	t.Setenv("WFX_RUNNER", "")
	if st, out := callCmd(t, h, "aos.file-read", map[string]any{"machine": "box", "path": "/etc/hosts"}); st != "failed" {
		t.Fatalf("off a worker: %s %s", st, out)
	}
	if len(calls()) != 0 {
		t.Fatalf("aos ran off a worker: %v", calls())
	}

	t.Setenv("WFX_RUNNER", "box")
	if st, out := callCmd(t, h, "aos.file-read", map[string]any{"machine": "box", "path": "/etc/hosts", "lines": "1:3"}); st != "done" || !strings.Contains(out, "file contents") {
		t.Fatalf("file-read: %s %s", st, out)
	}
	if c := calls(); len(c) != 1 || argsOf(c[0]) != "file read /etc/hosts --lines=1:3" {
		t.Fatalf("file-read call: %v", c)
	}
	if st, out := callCmd(t, h, "aos.screenshot", map[string]any{"machine": "box", "out": "s.png", "app": "Chrome"}); st != "done" || !strings.Contains(out, `"out": "s.png"`) {
		t.Fatalf("screenshot: %s %s", st, out)
	}
	if c := calls(); len(c) != 2 || argsOf(c[1]) != "capture screenshot --app=Chrome --out=s.png" {
		t.Fatalf("screenshot call: %v", c)
	}
	// Writes are not offered; nothing runs.
	for name, in := range map[string]map[string]any{
		"aos.shell":      {"machine": "box", "script": "rm -rf /tmp/x"},
		"aos.file-write": {"machine": "box", "path": "/tmp/a", "content": "hi"},
	} {
		if w := do(h, "POST", "/api/modules/"+name+"/call", "", map[string]any{"inputs": in, "wait": true}); w.Code == 201 {
			t.Fatalf("%s started: %s", name, w.Body)
		}
	}
	if len(calls()) != 2 {
		t.Fatalf("a write ran: %v", calls())
	}
}
