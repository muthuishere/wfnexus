package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localRunner is the fake ssh: it runs the on-host command here, with HOME
// pointing at a temp dir and fake systemctl / curl / sqlite3 / loginctl on
// PATH, so the REAL deploy script is what gets tested.
type localRunner struct {
	home, bin string
	env       []string
}

func (l localRunner) run(cmd string, stdin io.Reader) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = stdin
	c.Env = append([]string{
		"HOME=" + l.home,
		"PATH=" + l.bin + ":/usr/bin:/bin",
		"XDG_RUNTIME_DIR=" + l.home,
		"WFX_DEPLOY_WAIT=2",
		"WFX_CGROUP_ROOT=" + filepath.Join(l.home, "cg"),
	}, l.env...)
	out, err := c.Output()
	return string(out), err
}

func fakeHost(t *testing.T, systemUnit string) localRunner {
	t.Helper()
	t.Setenv("WFX_DEPLOY_SKIP_CLIS", "1")
	home, bin := t.TempDir(), t.TempDir()
	log := filepath.Join(home, "calls.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("systemctl", `echo "systemctl $*" >> "`+log+`"
case "$*" in
  "is-active wfnexus.service") echo "`+systemUnit+`"; [ "`+systemUnit+`" = active ] ;;
  "--user is-active"*) echo active ;;
  *"-p MemoryMax"*) echo 34359738368 ;;
  *"-p CPUQuotaPerSecUSec"*) echo 6s ;;
  *) exit 0 ;;
esac`)
	write("loginctl", `echo yes`)
	write("sqlite3", `echo "sqlite3 $*" >> "`+log+`"; f=$(echo "$2" | sed "s/^.backup '//; s/'$//"); cp "$1" "$f"`)
	// The "server": healthy unless the binary that current points at says BROKEN.
	write("curl", `B="$HOME/.local/share/wfnexus/current/wfx-server"
grep -q BROKEN "$B" && exit 7
case "$*" in
  *doctor*) grep -q NOMODEL "$B" && echo '{"probe":{"provider":"claude","ok":false,"error":"no key"}}' || echo '{"probe":{"provider":"claude","ok":true}}' ;;
  *) echo '{"ok":true}' ;;
esac`)
	data := filepath.Join(home, ".local/share/wfnexus")
	os.MkdirAll(data, 0o755)
	os.WriteFile(filepath.Join(data, "wfnexus.db"), []byte("db"), 0o644)
	return localRunner{home: home, bin: bin}
}

func fakeBinary(version, flaw string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\n# %s\necho 'wfx-server %s'\n", flaw, version))
}

func useFakeRelease(t *testing.T, flaw map[string]string) {
	t.Helper()
	old := fetchRelease
	fetchRelease = func(repo, version, arch string) (string, []byte, error) {
		if arch != "amd64" && arch != "arm64" {
			return "", nil, fmt.Errorf("arch %q", arch)
		}
		return version, fakeBinary(version, flaw[version]), nil
	}
	t.Cleanup(func() { fetchRelease = old })
}

// ageReleases pushes existing release dirs back in time so ls -t is ordered.
func ageReleases(t *testing.T, h localRunner) {
	dir := filepath.Join(h.home, ".local/share/wfnexus/releases")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		st, _ := os.Stat(p)
		old := st.ModTime().Add(-10 * time.Second)
		os.Chtimes(p, old, old)
	}
}

func current(t *testing.T, h localRunner) string {
	l, err := os.Readlink(filepath.Join(h.home, ".local/share/wfnexus/current"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(l)
}

func deployTo(t *testing.T, h localRunner, v string) error {
	ageReleases(t, h)
	return serverDeploy(h, serverDeployOpts{host: "box", version: v, repo: "x/y"})
}

func TestServerDeployInstallsUpgradesAndBacksUp(t *testing.T) {
	useFakeRelease(t, nil)
	h := fakeHost(t, "inactive")
	for _, v := range []string{"v0.9.1", "v0.9.2"} {
		if err := deployTo(t, h, v); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	if c := current(t, h); c != "v0.9.2" {
		t.Fatalf("current = %s", c)
	}
	unit, err := os.ReadFile(filepath.Join(h.home, ".config/systemd/user/wfnexus.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"ExecStart=%h/.local/share/wfnexus/current/wfx-server", "MemoryMax=32G", "CPUQuota=600%", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), w) {
			t.Errorf("unit lacks %q", w)
		}
	}
	if strings.Contains(string(unit), "User=") {
		t.Error("a user unit must not set User=")
	}
	backups, _ := filepath.Glob(filepath.Join(h.home, ".local/share/wfnexus/backups/*.db"))
	if len(backups) != 2 {
		t.Errorf("want a backup per swap, got %v", backups)
	}
	log, _ := os.ReadFile(filepath.Join(h.home, "calls.log"))
	if !strings.Contains(string(log), "systemctl --user restart wfnexus.service") {
		t.Errorf("no user restart: %s", log)
	}
	if strings.Contains(string(log), "sudo") {
		t.Error("deploy must never use sudo")
	}
}

func TestServerDeployHealthFailRollsBack(t *testing.T) {
	useFakeRelease(t, map[string]string{"v0.9.2": "BROKEN"})
	h := fakeHost(t, "inactive")
	if err := deployTo(t, h, "v0.9.1"); err != nil {
		t.Fatal(err)
	}
	err := deployTo(t, h, "v0.9.2")
	if err == nil || !strings.Contains(err.Error(), "rolled back to v0.9.1") {
		t.Fatalf("want a rollback, got %v", err)
	}
	if c := current(t, h); c != "v0.9.1" {
		t.Fatalf("current = %s after a failed deploy", c)
	}
}

func TestServerDeployModelNotReadyRollsBack(t *testing.T) {
	useFakeRelease(t, map[string]string{"v0.9.2": "NOMODEL"})
	h := fakeHost(t, "inactive")
	deployTo(t, h, "v0.9.1")
	if err := deployTo(t, h, "v0.9.2"); err == nil || !strings.Contains(err.Error(), "model not ready") {
		t.Fatalf("want model-not-ready rollback, got %v", err)
	}
	if c := current(t, h); c != "v0.9.1" {
		t.Fatalf("current = %s", c)
	}
	// --skip-model deploys it anyway.
	ageReleases(t, h)
	if err := serverDeploy(h, serverDeployOpts{host: "box", version: "v0.9.2", skipModel: true}); err != nil {
		t.Fatal(err)
	}
}

func TestServerDeployRefusesWhileSystemUnitRuns(t *testing.T) {
	useFakeRelease(t, nil)
	h := fakeHost(t, "active")
	err := deployTo(t, h, "v0.9.1")
	if err == nil || !strings.Contains(err.Error(), "systemctl disable --now wfnexus.service") {
		t.Fatalf("want the one-time root command, got %v", err)
	}
	if _, e := os.Lstat(filepath.Join(h.home, ".local/share/wfnexus/current")); e == nil {
		t.Error("nothing may be swapped while the system unit runs")
	}
	log, _ := os.ReadFile(filepath.Join(h.home, "calls.log"))
	if strings.Contains(string(log), "disable") {
		t.Error("the root command is printed, never run")
	}
}

func TestServerKeepsThreeReleasesAndRollsBackOne(t *testing.T) {
	useFakeRelease(t, nil)
	h := fakeHost(t, "inactive")
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		if err := deployTo(t, h, v); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(h.home, ".local/share/wfnexus/releases"))
	if len(ents) != 3 {
		t.Fatalf("want 3 releases kept, got %d", len(ents))
	}
	if err := serverRollback(h, serverDeployOpts{host: "box"}); err != nil {
		t.Fatal(err)
	}
	if c := current(t, h); c != "v3" {
		t.Fatalf("rollback went to %s, want v3", c)
	}
}

func TestServerStatusWarnsWhenLimitsNotEnforced(t *testing.T) {
	useFakeRelease(t, nil)
	h := fakeHost(t, "inactive")
	if err := deployTo(t, h, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := serverStatus(h, "box"); err == nil || !strings.Contains(err.Error(), "not enforced") {
		t.Fatalf("no cgroup controllers delegated: want a loud failure, got %v", err)
	}
	// Delegated: status is clean.
	id := strings.TrimSpace(mustOut(t, "id", "-u"))
	cg := filepath.Join(h.home, "cg/user.slice/user-"+id+".slice/user@"+id+".service")
	os.MkdirAll(cg, 0o755)
	os.WriteFile(filepath.Join(cg, "cgroup.controllers"), []byte("cpuset cpu io memory pids\n"), 0o644)
	if err := serverStatus(h, "box"); err != nil {
		t.Fatal(err)
	}
}

func TestServerDeployOldSSHInstallPathIsGone(t *testing.T) {
	o, err := parseDeploy([]string{"server", "--to", "ssh://ops@box"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planDeploy(o); err == nil || !strings.Contains(err.Error(), "wfx server deploy") {
		t.Fatalf("want a pointer to wfx server deploy, got %v", err)
	}
}

func TestVerifySum(t *testing.T) {
	bin := []byte("x")
	sums := "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881  wfx-server-linux-amd64\n"
	if err := verifySum([]byte(sums), "wfx-server-linux-amd64", bin); err != nil {
		t.Fatal(err)
	}
	if err := verifySum([]byte(sums), "wfx-server-linux-amd64", []byte("y")); err == nil {
		t.Fatal("a tampered binary must fail")
	}
}

func mustOut(t *testing.T, name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
