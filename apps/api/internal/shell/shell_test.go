package shell

import (
	"runtime"
	"strings"
	"testing"
)

// The default must exist on any machine this is developed or deployed on.
func TestDefaultResolvesOnThisMachine(t *testing.T) {
	sh, err := Default()
	if err != nil {
		t.Fatalf("no shell resolved on %s: %v", runtime.GOOS, err)
	}
	if !sh.Available() {
		t.Fatalf("resolved %q with no path", sh.Name)
	}
	argv := sh.Command("echo hi")
	if len(argv) < 3 || argv[0] != sh.Path || argv[len(argv)-1] != "echo hi" {
		t.Fatalf("argv = %v", argv)
	}
}

// `-l` sources the operator's profile, so the command would see a PATH and an
// environment that differ per developer. A workflow step is written down
// precisely so it does not depend on somebody's dotfiles.
func TestNoLoginShell(t *testing.T) {
	for _, sh := range Report() {
		for _, f := range sh.Flags {
			if f == "-l" || f == "-lc" || strings.Contains(f, "l") && f == "-il" {
				t.Errorf("%s uses a login flag %q", sh.Name, f)
			}
		}
	}
}

// POSIX shells come first on every platform, Windows included: Git for Windows
// and WSL both provide one, and a workflow written once should keep working.
func TestPosixIsPreferredEverywhere(t *testing.T) {
	first := candidates()[0]
	if first.Name != "bash" {
		t.Fatalf("first candidate on %s is %q, want bash", runtime.GOOS, first.Name)
	}
	if runtime.GOOS == "windows" {
		var names []string
		for _, s := range candidates() {
			names = append(names, s.Name)
		}
		for _, want := range []string{"bash", "sh", "pwsh", "powershell", "cmd"} {
			if !contains(names, want) {
				t.Errorf("windows candidates miss %q: %v", want, names)
			}
		}
	}
}

// A step naming a shell that is not installed, and a step naming one we have
// never heard of, are different problems and neither is "your command failed".
func TestNamedShellErrorsAreDistinct(t *testing.T) {
	if _, err := Lookup("definitely-not-a-shell"); err == nil {
		t.Fatal("unknown shell accepted")
	} else if !strings.Contains(err.Error(), "unknown shell") {
		t.Fatalf("want an unknown-shell error, got %v", err)
	}

	// `cmd` is a real name we support and is absent off Windows, which is the
	// named-but-not-installed case.
	if runtime.GOOS != "windows" {
		if _, err := Lookup("cmd"); err == nil {
			t.Fatal("cmd resolved off Windows")
		} else if !strings.Contains(err.Error(), "not on PATH") {
			t.Fatalf("want a not-installed error, got %v", err)
		}
	}
}

func TestWFXShellOverride(t *testing.T) {
	t.Setenv("WFX_SHELL", "sh")
	sh, err := Default()
	if err != nil {
		t.Skipf("sh not present: %v", err)
	}
	if sh.Name != "sh" {
		t.Fatalf("override ignored: %q", sh.Name)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
