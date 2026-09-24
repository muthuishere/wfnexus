package buildinfo

import "testing"

// A binary must always answer. "dev" is a true answer for an unstamped build;
// an empty string is not an answer at all, and it is what a version check in a
// bug report template would silently accept.
func TestGetAlwaysReportsAVersion(t *testing.T) {
	if got := Get().Version; got == "" {
		t.Fatal("Version is empty; an unstamped build must still report something")
	}
}

func TestStampedValuesWin(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })

	Version, Commit, Date = "v1.2.3", "abcdef1234567890", "2026-09-25"
	got := Get()
	if got.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", got.Version)
	}
	if got.Commit != "abcdef1234567890" {
		t.Errorf("Commit = %q, want the stamped one", got.Commit)
	}
	if got.Date != "2026-09-25" {
		t.Errorf("Date = %q, want the stamped one", got.Date)
	}
}

func TestStringAbbreviatesTheCommit(t *testing.T) {
	i := Info{Version: "v1.2.3", Commit: "abcdef1234567890", Date: "2026-09-25", Go: "go1.26.0"}
	want := "v1.2.3 (abcdef1, 2026-09-25, go1.26.0)"
	if got := i.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringWithNothingButAVersion(t *testing.T) {
	if got := (Info{Version: "dev"}).String(); got != "dev" {
		t.Errorf("String() = %q, want dev", got)
	}
}

func TestStringKeepsDirtyButShortensTheHash(t *testing.T) {
	i := Info{Version: "dev", Commit: "a71603f3bc79807719e447a709bbcdecebe92946-dirty"}
	want := "dev (a71603f-dirty)"
	if got := i.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
