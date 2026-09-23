package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// `wfx env` — the platform's own environment, system-wide or per project.
//
// A value is never echoed back by any of these commands, and `set` reads it
// from a prompt with echo off by default rather than from the argv, because an
// argv is in the shell history and in `ps`.

type envVar struct {
	Key       string    `json:"key"`
	Secret    bool      `json:"secret"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type envList struct {
	Scope     string   `json:"scope"`
	ScopeName string   `json:"scopeName"`
	Vars      []envVar `json:"vars"`
	KeySource string   `json:"keySource"`
}

func envCmd(args []string) error {
	project := flagOf(args, "--project", "")
	base := "/api/env"
	if project != "" {
		base = "/api/projects/" + url.PathEscape(project) + "/env"
	}
	rest := withoutFlag(args, "--project")

	switch {
	case len(rest) == 0 || rest[0] == "list":
		return envShow(base, project)
	case rest[0] == "set" && len(rest) >= 2:
		return envSet(base, rest[1:])
	case (rest[0] == "rm" || rest[0] == "unset") && len(rest) >= 2:
		if err := call("DELETE", base+"/"+url.PathEscape(rest[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", rest[1])
		return nil
	case rest[0] == "key":
		return fmt.Errorf("generate one with: openssl rand -base64 32 — then set WFX_SECRET_KEY")
	}
	return fmt.Errorf("usage: wfx env [--project p] [list | set NAME [--plain] | rm NAME]")
}

func envShow(base, project string) error {
	var out envList
	if err := call("GET", base, nil, &out); err != nil {
		return err
	}
	where := "system"
	if project != "" {
		where = "project " + project
	}
	if len(out.Vars) == 0 {
		fmt.Printf("no variables set for %s\n", where)
	} else {
		fmt.Printf("%-28s %-10s %s\n", "NAME", "KIND", "VALUE")
		for _, v := range out.Vars {
			kind, val := "secret", "—"
			if !v.Secret {
				kind, val = "plain", v.Value
			}
			fmt.Printf("%-28s %-10s %s\n", v.Key, kind, val)
		}
	}
	if out.KeySource != "" {
		fmt.Printf("\nencrypted with the key from %s\n", out.KeySource)
	}
	fmt.Printf("\nlayers: system → project → workflow → job → step\n")
	return nil
}

func envSet(base string, args []string) error {
	name := args[0]
	plain := hasFlag(args, "--plain")
	value := flagOf(args, "--value", "")
	if value == "" {
		// Read it rather than take it from argv: an argv is in the shell
		// history and visible in `ps` to anyone on the machine.
		v, err := readSecret(fmt.Sprintf("value for %s (not echoed): ", name))
		if err != nil {
			return err
		}
		value = v
	}
	if value == "" {
		return fmt.Errorf("no value given")
	}
	body := map[string]any{"key": name, "value": value, "secret": !plain}
	if err := call("PUT", base, body, nil); err != nil {
		return err
	}
	kind := "secret"
	if plain {
		kind = "plain"
	}
	fmt.Printf("%s set (%s)\n", name, kind)
	return nil
}

func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	// Piped in — `echo $TOKEN | wfx env set X` — which keeps it out of argv too.
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(line), err
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// withoutFlag drops a `--flag value` pair, leaving the positional arguments.
func withoutFlag(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
