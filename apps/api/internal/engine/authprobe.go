package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// authVerdict is what a vendor's own status command answered. unknown means
// the check could not tell logged-out from broken, and that is not a login
// failure — design decision 4: a check that cannot distinguish the two must
// stay present, never ready.
type authVerdict int

const (
	authUnknown authVerdict = iota
	authReady
	authLoggedOut
)

// authProbe is one vendor's cheap, unambiguous "am I logged in?". The table
// is built by running the vendors, not by guessing their flags. A vendor
// with no entry stays present. Tried, and left out, on 2026-09-25:
//
//   - opencode: `opencode auth list` exits 0 with credentials and exits 0
//     with none. A zero count is not "logged out" — env-backed providers
//     still appear — so the two states cannot be told apart.
//   - copilot: there is no status command. `copilot auth status` is parsed
//     as a prompt, and `copilot login` has no query form. gh's login is a
//     different credential store, so `gh auth status` is not this binary's
//     answer.
type authProbe struct {
	args []string
	// discardStdout is set when a successful run prints a credential. The
	// verdict then comes from the exit code and stderr only, so the value
	// never enters this process as a string.
	discardStdout bool
	classify      func(code int, stdout, stderr string) authVerdict
}

// authProbes is keyed by the binary name, not the provider name. What was
// tried, and why each entry is here, is recorded in the change's tasks.md.
var authProbes = map[string]authProbe{
	"claude": {
		args: []string{"auth", "status"},
		classify: func(code int, stdout, _ string) authVerdict {
			var body struct {
				LoggedIn *bool `json:"loggedIn"`
			}
			if json.Unmarshal([]byte(stdout), &body) != nil || body.LoggedIn == nil {
				return authUnknown
			}
			if *body.LoggedIn {
				return authReady
			}
			return authLoggedOut
		},
	},
	"codex": {
		args: []string{"login", "status"},
		classify: func(code int, _, stderr string) authVerdict {
			msg := strings.TrimSpace(stderr)
			if code == 0 && strings.HasPrefix(msg, "Logged in") {
				return authReady
			}
			if code != 0 && msg == "Not logged in" {
				return authLoggedOut
			}
			return authUnknown
		},
	},
	"gh": {
		args:          []string{"auth", "status"},
		discardStdout: true, // a logged-in run prints a token
		classify: func(code int, _, stderr string) authVerdict {
			if code == 0 {
				return authReady
			}
			if strings.Contains(stderr, "You are not logged into any GitHub hosts") {
				return authLoggedOut
			}
			return authUnknown
		},
	},
	"devin": {
		args: []string{"auth", "status"},
		classify: func(_ int, stdout, _ string) authVerdict {
			// Exit status is not the signal: logged-out also exits 0.
			// The first line is, and "Not logged in" contains "Logged in",
			// so the negative is matched first. Anything else — a path, an
			// email, a credential file — is not read.
			line, _, _ := strings.Cut(stdout, "\n")
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Not logged in"):
				return authLoggedOut
			case strings.HasPrefix(line, "Logged in"):
				return authReady
			default:
				return authUnknown
			}
		},
	},
}

// probeAuth runs one vendor's status command against the binary LookPath
// already found. A timeout or a failure to start is unknown, not logged-out:
// those are "the check broke", and claiming a login state from them is the
// lie this table exists to avoid.
func probeAuth(name, path string) authVerdict {
	spec, ok := authProbes[filepath.Base(name)]
	if !ok {
		return authUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, spec.args...)
	var stdout, stderr bytes.Buffer
	if spec.discardStdout {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return authUnknown
		}
	}
	return spec.classify(code, stdout.String(), stderr.String())
}
