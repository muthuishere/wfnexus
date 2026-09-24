// Package buildinfo is what a binary answers "what are you?" with.
//
// A build that cannot say which version it is cannot be supported: the first
// question about any bug report is which binary produced it, and "the one I
// downloaded" is not an answer.
//
// TWO SOURCES, IN THAT ORDER. The Taskfile and the release workflow stamp
// Version/Commit/Date with -X at link time, which is the accurate path because
// the tag is what a release IS. But `go install …/cmd/wfx@latest` is a
// documented install path and it passes no ldflags, so a stamped-only binary
// would report "dev" for a version the user genuinely installed by tag. The
// fallback reads what the toolchain already embedded — module version and the
// vcs.* settings — so that path reports something true as well.
//
// Nothing here fails. A binary built by neither route (a plain `go build`
// during development) says "dev", which is also true.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Stamped by -X at link time. Never assign to these anywhere else: the linker
// is the only writer, and a second one would make the value a guess.
var (
	Version string
	Commit  string
	Date    string
)

// Info is the resolved answer, after the fallback has filled what -X did not.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
	Go      string `json:"go,omitempty"`
}

// Get resolves the build's identity, preferring the linker's values.
func Get() Info {
	out := Info{Version: Version, Commit: Commit, Date: Date}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if out.Version == "" {
			out.Version = "dev"
		}
		return out
	}
	out.Go = bi.GoVersion

	// `go install pkg@v1.2.3` records the module version here. "(devel)" means
	// it was built from a working tree, which tells us nothing a stamp would
	// not have told us better, so it is not treated as a version.
	if out.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		out.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if out.Commit == "" {
				out.Commit = s.Value
			}
		case "vcs.time":
			if out.Date == "" {
				out.Date = s.Value
			}
		case "vcs.modified":
			// An uncommitted tree is not the commit it claims to be, and a bug
			// report that says otherwise wastes the time of whoever reads it.
			if s.Value == "true" && out.Commit != "" && !strings.HasSuffix(out.Commit, "-dirty") {
				out.Commit += "-dirty"
			}
		}
	}
	if out.Version == "" {
		out.Version = "dev"
	}
	return out
}

// String is the one-line form: "v0.1.0 (a71603f, 2026-09-25, go1.26.0)".
func (i Info) String() string {
	out := i.Version
	var parts []string
	if i.Commit != "" {
		// Abbreviate the hash but keep the "-dirty" marker: a 40-character
		// hash is unreadable in a one-line version string, and dropping the
		// marker would let an uncommitted build claim to be that commit.
		c, suffix := i.Commit, ""
		if rest, ok := strings.CutSuffix(c, "-dirty"); ok {
			c, suffix = rest, "-dirty"
		}
		if len(c) > 7 {
			c = c[:7]
		}
		parts = append(parts, c+suffix)
	}
	if i.Date != "" {
		parts = append(parts, i.Date)
	}
	if i.Go != "" {
		parts = append(parts, i.Go)
	}
	if len(parts) > 0 {
		out += " (" + strings.Join(parts, ", ") + ")"
	}
	return out
}
