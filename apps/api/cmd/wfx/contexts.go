package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Where the client keeps hosts and their credentials.
//
// The precedent is `gh` (~/.config/gh/hosts.yml): ONE file, keyed by host,
// holding the token itself. Not docker's credential helper — that is a second
// component to ship on three platforms — and not kubectl's three cross-joined
// lists, because we have one axis: a host.
//
// The file holds a credential, so it is 0600 in a 0700 directory and the client
// REFUSES to read one that is group- or world-readable, the way ssh refuses a
// loose private key. A warning is not a mitigation.

type wfxContext struct {
	URL     string `json:"url"`
	Token   string `json:"token,omitempty"`
	User    string `json:"user,omitempty"`
	Project string `json:"project,omitempty"`
	Created string `json:"createdAt,omitempty"`
}

type contextsFile struct {
	Current  string                 `json:"current"`
	Contexts map[string]*wfxContext `json:"contexts"`
}

func contextsPath() string {
	if p := os.Getenv("WFX_CONTEXTS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "wfx", "contexts.json")
}

// loadContexts reads the file, or returns an empty one when there is none.
// A file anyone else on the machine can read is an ERROR, not a warning: it
// holds a bearer token, and every request this client makes would then be
// something another local user could make too.
func loadContexts() (*contextsFile, error) {
	path := contextsPath()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &contextsFile{Contexts: map[string]*wfxContext{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("%s is %#o — it holds a credential and must be readable only by you.\n"+
			"Fix it with: chmod 600 %s", path, mode, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &contextsFile{}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("%s is not readable as JSON: %w", path, err)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]*wfxContext{}
	}
	return c, nil
}

// saveContexts writes via a temp file and os.Rename — the pattern
// internal/blob/folder.go already uses — so a reader never sees a half-written
// file and a crash leaves no truncated one. The temp file is created 0600 from
// the start: a window in which the credential is world-readable is the same bug
// as the file being world-readable.
func saveContexts(c *contextsFile) error {
	path := contextsPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".contexts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// hostKey names a context by its host, so `--url https://wfx.example.com/` and
// `--url https://wfx.example.com` are one context rather than two.
func hostKey(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return u.Host
}

func normalizeURL(raw string) string { return strings.TrimRight(raw, "/") }

// resolved is the host a command talks to and the credential it presents.
type resolved struct {
	url   string
	token string
	name  string
}

// resolveContext is the precedence rule, most specific first:
//
//  1. --url on the command line, which selects the named context.
//  2. WFX_API — KEPT, unchanged, so every existing script and the localhost
//     case keep working. A WFX_API with no matching context sends no bearer,
//     which is exactly right on loopback.
//  3. contexts.current
//  4. http://127.0.0.1:8090
func resolveContext(urlFlag string) (resolved, error) {
	c, err := loadContexts()
	if err != nil {
		return resolved{}, err
	}
	if urlFlag != "" {
		key := hostKey(urlFlag)
		ctx, ok := c.Contexts[key]
		if !ok {
			return resolved{}, fmt.Errorf("no context for %s — run `wfx login --url %s` first", key, normalizeURL(urlFlag))
		}
		return resolved{url: ctx.URL, token: ctx.Token, name: key}, nil
	}
	if v := os.Getenv("WFX_API"); v != "" {
		r := resolved{url: normalizeURL(v), name: hostKey(v)}
		// A context for that same host lends its credential; without one this
		// is the unchanged, tokenless WFX_API path.
		if ctx, ok := c.Contexts[r.name]; ok && normalizeURL(ctx.URL) == r.url {
			r.token = ctx.Token
		}
		return r, nil
	}
	if c.Current != "" {
		if ctx, ok := c.Contexts[c.Current]; ok {
			return resolved{url: ctx.URL, token: ctx.Token, name: c.Current}, nil
		}
	}
	return resolved{url: "http://127.0.0.1:8090", name: "127.0.0.1:8090"}, nil
}

// putContext adds or replaces ONE context. Every other context's credential is
// rewritten byte for byte as it was read, so logging in to B cannot disturb A.
func putContext(hostURL, token, user string) error {
	c, err := loadContexts()
	if err != nil {
		return err
	}
	key := hostKey(hostURL)
	c.Contexts[key] = &wfxContext{
		URL: normalizeURL(hostURL), Token: token, User: user,
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	c.Current = key
	return saveContexts(c)
}

func removeContext(key string) error {
	c, err := loadContexts()
	if err != nil {
		return err
	}
	delete(c.Contexts, key)
	if c.Current == key {
		c.Current = ""
		for k := range c.Contexts {
			c.Current = k
			break
		}
	}
	return saveContexts(c)
}

// contextCmd is `wfx context [list|use <host>|rm <host>]`.
func contextCmd(args []string) error {
	c, err := loadContexts()
	if err != nil {
		return err
	}
	switch first(args) {
	case "", "list", "ls":
		if len(c.Contexts) == 0 {
			fmt.Println("no contexts — run `wfx login --url <host>`")
			return nil
		}
		keys := make([]string, 0, len(c.Contexts))
		for k := range c.Contexts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			mark := "  "
			if k == c.Current {
				mark = "* "
			}
			state := "not authenticated"
			if c.Contexts[k].Token != "" {
				// Metadata only. The value is never printed, here or anywhere.
				state = "authenticated as " + or(c.Contexts[k].User, "?")
			}
			fmt.Printf("%s%-30s %-34s %s\n", mark, k, c.Contexts[k].URL, state)
		}
		return nil
	case "use":
		key := hostKey(args[len(args)-1])
		if _, ok := c.Contexts[key]; !ok {
			return fmt.Errorf("no context for %s — run `wfx login --url …` first", key)
		}
		c.Current = key
		return saveContexts(c)
	case "rm", "remove":
		return removeContext(hostKey(args[len(args)-1]))
	}
	return fmt.Errorf("usage: wfx context [list|use <host>|rm <host>]")
}
