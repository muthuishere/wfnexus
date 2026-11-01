package catalog

import (
	"fmt"
	"strings"
)

// SaveMcp writes or replaces one MCP server in path (mcp.json). A server's
// credentials are NAMES (EnvFrom), resolved from the env store when a step is
// built; a literal value that looks like a key is refused.
func SaveMcp(path string, m McpServer) error {
	if m.Name == "" {
		return fmt.Errorf("an mcp server needs a name")
	}
	if err := ValidateMcp(m); err != nil {
		return err
	}
	return mutate(path, func(f *file) {
		if f.McpServers == nil {
			f.McpServers = map[string]McpServer{}
		}
		stored := m
		stored.Name = ""
		f.McpServers[m.Name] = stored
	})
}

// DeleteMcp removes an MCP server from path.
func DeleteMcp(path, name string) error {
	f, err := readFile(path)
	if err != nil {
		return err
	}
	if _, ok := f.McpServers[name]; !ok {
		return fmt.Errorf("no mcp server named %q", name)
	}
	return mutate(path, func(f *file) { delete(f.McpServers, name) })
}

// ValidateMcp is the shape rule: a local server runs a command, a remote one
// has a URL, every EnvFrom entry is a variable NAME, and no literal value in
// Environment or Headers looks like a credential. Messages never echo a value.
func ValidateMcp(m McpServer) error {
	switch m.Type {
	case "", "local":
		if len(m.Command) == 0 {
			return fmt.Errorf("mcp server %q: a local server needs a command (--set command=\"npx -y pkg\")", m.Name)
		}
	case "remote":
		if m.URL == "" {
			return fmt.Errorf("mcp server %q: a remote server needs a url (--set url=https://…)", m.Name)
		}
	default:
		return fmt.Errorf("mcp server %q: type must be local or remote, got %q", m.Name, m.Type)
	}
	for _, n := range m.EnvFrom {
		if n == "" || looksLikeSecret(n) {
			return fmt.Errorf("mcp server %q: --env takes the NAME of an env-store key, not a value", m.Name)
		}
	}
	for k, v := range m.Environment {
		if LooksLikeCredential(v) {
			return fmt.Errorf("mcp server %q: env.%s looks like a credential — store it with `wfx env set %s` and pass --env %s", m.Name, k, k, k)
		}
	}
	for k, v := range m.Headers {
		if LooksLikeCredential(v) {
			return fmt.Errorf("mcp server %q: header %s looks like a credential — headers hold no secrets here", m.Name, k)
		}
	}
	return nil
}

// LooksLikeCredential is a cheap guard for a literal VALUE (not a name): a
// well-known key prefix, or a long opaque token. It never echoes the value.
func LooksLikeCredential(v string) bool {
	lv := strings.ToLower(v)
	for _, p := range []string{"sk-", "ghp_", "gho_", "github_pat_", "xoxb-", "xoxp-", "bearer ", "akia", "wfx_"} {
		if strings.HasPrefix(lv, p) {
			return true
		}
	}
	return len(v) >= 32 && !strings.ContainsAny(v, " /:.")
}
