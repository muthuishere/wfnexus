package store

import (
	"testing"
)

// The bug this guards: "sqlite://" + a Windows path parsed "C:" as a host with
// an invalid port, so every migration failed before running a statement. Unix
// was correct only by accident. CI on windows-latest is what found it.
func TestSQLiteMigrateURLIsAURLOnEitherOS(t *testing.T) {
	for _, tc := range []struct{ name, dsn, want string }{
		{"unix absolute", "/home/u/.local/share/wfnexus/wfnexus.db",
			"sqlite:///home/u/.local/share/wfnexus/wfnexus.db?_pragma=busy_timeout(5000)"},
		{"windows absolute", `C:\Users\RUNNER~1\AppData\Local\Temp\wfnexus.db`,
			"sqlite:///C:/Users/RUNNER~1/AppData/Local/Temp/wfnexus.db?_pragma=busy_timeout(5000)"},
		{"relative", "wfnexus.db", "sqlite:///wfnexus.db?_pragma=busy_timeout(5000)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqliteMigrateURL(tc.dsn); got != tc.want {
				t.Errorf("sqliteMigrateURL(%q)\n got %q\nwant %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// A path with a space is ordinary on both platforms ("Application Support",
// "Program Files") and must survive as a URL rather than truncating.
func TestSQLiteMigrateURLEscapesASpace(t *testing.T) {
	got := sqliteMigrateURL("/Users/me/Library/Application Support/wfnexus.db")
	want := "sqlite:///Users/me/Library/Application%20Support/wfnexus.db?_pragma=busy_timeout(5000)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
