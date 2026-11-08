package api

import (
	"strings"
	"testing"
)

// aos module commands through `wfx module call` against a stub aos. They run
// only on a worker (WFX_RUNNER, set by wfx-runner; "box" here is served in
// process to stand in for one), and shell / file-write always wait for a
// person.
func TestAosCommandWrappers(t *testing.T) {
	h, eng, tmp := aplServer(t, nil)
	for _, s := range eng.SourceSkips() {
		t.Errorf("skipped: %s %s", s.Workflow, s.Reason)
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
	// Writes wait for a person; nothing runs before.
	if st, _ := callCmd(t, h, "aos.shell", map[string]any{"machine": "box", "script": "rm -rf /tmp/x"}); st != "awaiting_approval" {
		t.Fatalf("shell: %s", st)
	}
	if st, _ := callCmd(t, h, "aos.file-write", map[string]any{"machine": "box", "path": "/tmp/a", "content": "hi"}); st != "awaiting_approval" {
		t.Fatalf("file-write: %s", st)
	}
	if len(calls()) != 2 {
		t.Fatalf("a write ran before approval: %v", calls())
	}
}
