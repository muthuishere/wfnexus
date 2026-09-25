package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/muthuishere/wfnexus/apps/api/internal/buildinfo"
)

// `wfx repl` — the same client, without retyping the client.
//
// Every verb this CLI has is already a call against a host, so a session is the
// natural unit: you pick a host once and then work. The prompt shows which host,
// because the one mistake that matters here is running the right command against
// the wrong platform, and a REPL that hides its target invites it.
//
// It dispatches through `run`, the SAME function main uses. A REPL with its own
// command table would be a second CLI that drifts from the first — and it would
// drift silently, because nobody tests the copy. Everything therefore works in
// here the moment it works out there, including `context use`, which makes
// switching platforms mid-session free rather than a feature.
//
// One deliberate difference from main: an error prints and the session
// continues. main exits 1 because a script needs the status; a session that
// exited on a typo would be unusable.

// replPrompt is what the line looks like. Kept as a function so the test can
// read it rather than match a string in two places.
func replPrompt(host string) string {
	return host + " › "
}

func repl(args []string) error {
	// `--url` selects the host for the session, exactly as it does for one
	// command. Switching later is `context use`, which already exists.
	if u := flagOf(args, "--url", ""); u != "" {
		if err := contextCmd([]string{"use", u}); err != nil {
			return err
		}
	}
	in, out := io.Reader(os.Stdin), io.Writer(os.Stdout)
	if replIn != nil {
		in, out = replIn, replOut
	}
	// A terminal gets the banner; a pipe does not, so `echo runs | wfx repl`
	// produces output a script can read instead of decoration it has to strip.
	interactive := replIn == nil && term.IsTerminal(int(os.Stdin.Fd()))
	if interactive {
		fmt.Fprintf(out, "wfx %s — one host, many commands. `exit` to leave, `help` for the verbs.\n",
			buildinfo.Get().Version)
	}
	sc := bufio.NewScanner(in)
	for {
		if interactive {
			fmt.Fprint(out, replPrompt(base()))
		}
		if !sc.Scan() {
			if interactive {
				fmt.Fprintln(out)
			}
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch line {
		case "exit", "quit", ":q":
			return nil
		}
		fields, err := splitLine(line)
		if err != nil {
			fmt.Fprintln(out, "error: "+err.Error())
			continue
		}
		if len(fields) == 0 {
			continue
		}
		// `wfx runs` typed inside wfx is what everybody does at least once. Drop
		// it rather than report "unknown command wfx", which reads like a broken
		// install.
		if fields[0] == "wfx" {
			fields = fields[1:]
			if len(fields) == 0 {
				continue
			}
		}
		if fields[0] == "repl" {
			fmt.Fprintln(out, "already in a repl")
			continue
		}
		if err := run(fields); err != nil {
			// Printed, not returned: a session survives a typo.
			fmt.Fprintln(out, "error: "+err.Error())
		}
	}
}

// splitLine splits a typed line into argv, honouring quotes.
//
// strings.Fields is wrong here and wrong in a way that only shows up later: a
// prompt or an input value with a space in it (`-i title="fix the parser"`) is
// the ordinary case for this product, and Fields would hand three broken
// arguments to a verb that then fails somewhere far away.
func splitLine(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if started {
		out = append(out, cur.String())
	}
	return out, nil
}

// replIn / replOut let a test drive a session without a terminal. Nil means the
// real thing.
var (
	replIn  io.Reader
	replOut io.Writer
)
