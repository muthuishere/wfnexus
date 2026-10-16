package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// `wfx team` runs ON the server host, as the `wfx` user. It edits that user's
// ~/.ssh/authorized_keys so a teammate's key can open a tunnel to the API port
// and nothing else: no shell, no other forward, no agent or X11.

const teamTag = "wfx-team:"

func authorizedKeysPath() string {
	if p := os.Getenv("WFX_TEAM_KEYS_FILE"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), ".ssh", "authorized_keys")
}

// permitTarget is the loopback host:port the server listens on.
func permitTarget() string {
	addr := config.Load().Addr
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8090"
	}
	return "127.0.0.1:" + port
}

func teamLine(name, pubkey, permit string) (string, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkey))
	if err != nil {
		return "", fmt.Errorf("not an SSH public key: %w", err)
	}
	if strings.ContainsAny(name, " \t\n\"") || name == "" {
		return "", fmt.Errorf("team name %q: no spaces or quotes", name)
	}
	k := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	return fmt.Sprintf(`restrict,port-forwarding,permitopen="%s" %s %s%s`, permit, k, teamTag, name), nil
}

func lineTeam(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	last := f[len(f)-1]
	if strings.HasPrefix(last, teamTag) {
		return strings.TrimPrefix(last, teamTag)
	}
	return ""
}

func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// writeLines replaces the file atomically, 0600, in a 0700 directory.
func writeLines(path string, lines []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".authorized_keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// teamAdd sets <name>'s line: a second add replaces it, an identical one is a no-op.
func teamAdd(path, name, pubkey, permit string) (bool, error) {
	line, err := teamLine(name, pubkey, permit)
	if err != nil {
		return false, err
	}
	lines, err := readLines(path)
	if err != nil {
		return false, err
	}
	var out []string
	for _, l := range lines {
		if lineTeam(l) == name {
			if l == line {
				return false, nil
			}
			continue
		}
		out = append(out, l)
	}
	return true, writeLines(path, append(out, line))
}

func teamRemove(path, name string) (bool, error) {
	lines, err := readLines(path)
	if err != nil {
		return false, err
	}
	var out []string
	for _, l := range lines {
		if lineTeam(l) != name {
			out = append(out, l)
		}
	}
	if len(out) == len(lines) {
		return false, nil
	}
	return true, writeLines(path, out)
}

func teamCmd(args []string) error {
	path := authorizedKeysPath()
	switch first(args) {
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: wfx team add <name> <pubkey-file>")
		}
		raw, err := os.ReadFile(expandHome(args[2]))
		if err != nil {
			return err
		}
		changed, err := teamAdd(path, args[1], string(raw), permitTarget())
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("%s may tunnel to %s\n", args[1], permitTarget())
		} else {
			fmt.Printf("%s unchanged\n", args[1])
		}
		return nil
	case "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx team remove <name>")
		}
		changed, err := teamRemove(path, args[1])
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("%s removed\n", args[1])
		} else {
			fmt.Printf("%s was not there\n", args[1])
		}
		return nil
	case "", "list", "ls":
		lines, err := readLines(path)
		if err != nil {
			return err
		}
		n := 0
		for _, l := range lines {
			if t := lineTeam(l); t != "" {
				n++
				fmt.Println(t)
			}
		}
		if n == 0 {
			fmt.Println("no team keys — wfx team add <name> <pubkey-file>")
		}
		return nil
	}
	return fmt.Errorf("usage: wfx team [list|add <name> <pubkey-file>|remove <name>]")
}
