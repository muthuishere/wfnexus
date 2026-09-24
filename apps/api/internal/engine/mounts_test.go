package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func mustMount(t *testing.T, line string) workflow.Mount {
	t.Helper()
	m, err := workflow.ParseMount(line)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A folder with a file in it, somewhere a mount is allowed to point.
func hostFolder(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A read-only mount is READABLE from the step.
func TestReadOnlyMountIsVisibleInTheWorkspace(t *testing.T) {
	host := hostFolder(t, "fixtures", "hello from the host")
	ws := t.TempDir()

	_, roots, env, err := attachMounts(ws, t.TempDir(), []workflow.Mount{mustMount(t, host+":in:ro")})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(ws, "in", "note.txt"))
	if err != nil {
		t.Fatalf("the mounted file is not in the workspace: %v", err)
	}
	if string(got) != "hello from the host" {
		t.Fatalf("read %q", got)
	}
	// A read-only mount is a copy INSIDE the workspace, so it must not widen
	// containment by even one folder.
	if len(roots) != 0 {
		t.Fatalf("a read-only mount must not widen containment, got %v", roots)
	}
	if env["WFX_MOUNT_IN"] != filepath.Join(ws, "in") {
		t.Fatalf("WFX_MOUNT_IN = %q", env["WFX_MOUNT_IN"])
	}
}

// THE POINT OF `:ro`. Not "the guardrail would have denied it" — the step can
// write, and the host folder is still untouched afterwards.
func TestReadOnlyMountIsActuallyReadOnly(t *testing.T) {
	host := hostFolder(t, "fixtures", "original")
	ws := t.TempDir()
	if _, _, _, err := attachMounts(ws, t.TempDir(), []workflow.Mount{mustMount(t, host+":in:ro")}); err != nil {
		t.Fatal(err)
	}
	// Exactly what a `run:` step could do: no guardrail, no agent, a plain write.
	if err := os.WriteFile(filepath.Join(ws, "in", "note.txt"), []byte("clobbered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "in", "new.txt"), []byte("added"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ws, "in")); err != nil {
		t.Fatal(err)
	}
	back, err := os.ReadFile(filepath.Join(host, "note.txt"))
	if err != nil {
		t.Fatalf("the host folder was destroyed through a read-only mount: %v", err)
	}
	if string(back) != "original" {
		t.Fatalf("the host file changed through a read-only mount: %q", back)
	}
	if _, err := os.Stat(filepath.Join(host, "new.txt")); err == nil {
		t.Fatal("a file written into a read-only mount reached the host folder")
	}
}

// A writable mount is the opposite bargain, and it has to actually work.
func TestWritableMountReachesTheHostAndWidensContainment(t *testing.T) {
	host := hostFolder(t, "out", "before")
	ws := t.TempDir()
	_, roots, _, err := attachMounts(ws, t.TempDir(), []workflow.Mount{mustMount(t, host+":out:rw")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "out", "report.txt"), []byte("written by the run"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(host, "report.txt"))
	if err != nil || string(got) != "written by the run" {
		t.Fatalf("a writable mount must reach the host folder: %v %q", err, got)
	}
	if len(roots) != 1 {
		t.Fatalf("a writable mount must be a containment root, got %v", roots)
	}
	// And the guardrail must let the agent follow the link — otherwise the
	// mount exists and nothing can use it.
	rail := containmentGuardrail(ws, roots...)
	ev := tn.BeforeToolEvent{Name: "bash", Args: map[string]any{
		"command": "cat " + filepath.Join(host, "report.txt"),
	}}
	if deny := rail(ev); deny != "" {
		t.Fatalf("the agent may not read its own writable mount: %s", deny)
	}
	// Everything else outside the workspace is still denied. Widening is by
	// the declared folder, not by "outside is now fine".
	ev2 := tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "cat /tmp/other/secret"}}
	if rail(ev2) == "" {
		t.Fatal("a mount must widen containment by ITSELF, not switch it off")
	}
}

// The denylist. Each of these is somebody's credentials or the machine itself.
func TestForbiddenMounts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	for _, p := range []string{
		"/",
		home,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws", "cli"),
		filepath.Join(home, "projects", ".gnupg"),
		filepath.Join(home, ".config", "wfx"),
		"/etc",
		"/etc/ssl/private",
		"/usr/local/bin",
	} {
		if err := forbiddenMount(p); err == nil {
			t.Errorf("%s must be refused as a mount", p)
		}
	}
	// And the ordinary case is not refused, or the feature is unusable.
	for _, p := range []string{
		filepath.Join(home, "datasets", "2026"),
		filepath.Join(home, "work", "fixtures"),
	} {
		if err := forbiddenMount(p); err != nil {
			t.Errorf("%s must be allowed: %v", p, err)
		}
	}
}

// A symlink is the obvious way past a check on the spelling, so the check is
// not on the spelling.
func TestAMountCannotReachKeysThroughASymlink(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	ssh := filepath.Join(home, ".ssh")
	if _, err := os.Stat(ssh); err != nil {
		t.Skip("no ~/.ssh on this machine")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "innocent")
	if err := os.Symlink(ssh, link); err != nil {
		t.Skip(err)
	}
	if _, err := resolveMountHost(t.TempDir(), mustMount(t, link+":stuff:ro")); err == nil {
		t.Fatal("a symlink pointing at ~/.ssh must be refused, not followed")
	}
}

// A relative host resolves under the DATA DIR, and cannot climb out of it.
func TestRelativeMountResolvesUnderTheDataDir(t *testing.T) {
	data := t.TempDir()
	got, err := resolveMountHost(data, mustMount(t, "reports:out:rw"))
	if err != nil {
		t.Fatal(err)
	}
	want := resolveRoot(filepath.Join(data, MountsDirName, "reports"))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := resolveMountHost(data, mustMount(t, "../../../etc:x:ro")); err == nil {
		t.Fatal("a relative mount must not climb out of the data dir")
	}
}

// An absolute mount that is not on this machine fails BY NAME. A worker on
// another host is the case this exists for.
func TestAMissingMountSaysWhereItLooked(t *testing.T) {
	_, err := resolveMountHost(t.TempDir(), mustMount(t, "/definitely/not/here:x:ro"))
	if err == nil {
		t.Fatal("a mount that does not exist must fail")
	}
	if !strings.Contains(err.Error(), "/definitely/not/here") {
		t.Fatalf("the error must name the folder: %v", err)
	}
}

// ---- the workflow's own files ----

func TestSidecarFilesAreStagedAtTheWorkspaceRoot(t *testing.T) {
	ws := t.TempDir()
	files := []workflow.File{
		{Path: "run.js", Mode: 0o644, Body: []byte("console.log('hi')")},
		{Path: "lib/format.js", Mode: 0o644, Body: []byte("module.exports={}")},
	}
	if err := stageFiles(ws, files, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatalf("%s: %v", f.Path, err)
		}
		if string(got) != string(f.Body) {
			t.Fatalf("%s: %q", f.Path, got)
		}
	}
}

// A sidecar is an executable payload; it must not be able to land anywhere but
// the workspace.
func TestSidecarFilesCannotEscapeTheWorkspace(t *testing.T) {
	ws := t.TempDir()
	for _, p := range []string{"../escaped.js", "../../etc/cron.d/x", "/etc/cron.d/x"} {
		err := stageFiles(ws, []workflow.File{{Path: p, Body: []byte("x")}}, nil)
		if err == nil {
			t.Fatalf("%q must be refused", p)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(ws), "escaped.js")); statErr == nil {
			t.Fatalf("%q was written anyway", p)
		}
	}
}

// The two features stay separate: the workflow's own file never overwrites a
// folder somebody attached.
func TestSidecarCannotOverwriteAMountedFolder(t *testing.T) {
	host := hostFolder(t, "data", "precious")
	ws := t.TempDir()
	mounts := []workflow.Mount{mustMount(t, host+":data:rw")}
	if _, _, _, err := attachMounts(ws, t.TempDir(), mounts); err != nil {
		t.Fatal(err)
	}
	err := stageFiles(ws, []workflow.File{{Path: "data/note.txt", Body: []byte("from the repo")}}, mounts)
	if err == nil {
		t.Fatal("a sidecar landing inside a mount must be refused")
	}
	got, _ := os.ReadFile(filepath.Join(host, "note.txt"))
	if string(got) != "precious" {
		t.Fatalf("the mounted folder was written through: %q", got)
	}
}
