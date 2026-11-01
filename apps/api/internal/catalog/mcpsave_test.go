package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveMcpRoundTripAndRefusals(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(p, []byte(`{"_comment":["kept"],"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveMcp(p, McpServer{Name: "gh", Command: []string{"npx", "gh"}, EnvFrom: []string{"GITHUB_TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load("", p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Mcp.Get("gh")
	if !ok || got.EnvFrom[0] != "GITHUB_TOKEN" {
		t.Fatalf("got %+v", got)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "_comment") {
		t.Error("an unknown key must survive the write")
	}
	if err := DeleteMcp(p, "gh"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMcp(p, "gh"); err == nil {
		t.Error("deleting what is not there must say so")
	}

	for _, bad := range []McpServer{
		{Name: "a"},                 // no command
		{Name: "b", Type: "remote"}, // no url
		{Name: "c", Command: []string{"x"}, EnvFrom: []string{"ghp_notaname value"}},
		{Name: "d", Command: []string{"x"}, Environment: map[string]string{"TOKEN": "ghp_0123456789abcdef"}},
		{Name: "e", Type: "remote", URL: "https://x", Headers: map[string]string{"Authorization": "Bearer abc"}},
	} {
		err := SaveMcp(p, bad)
		if err == nil {
			t.Errorf("%s must be refused", bad.Name)
			continue
		}
		if strings.Contains(err.Error(), "ghp_0123") || strings.Contains(err.Error(), "Bearer abc") {
			t.Errorf("%s: the refusal echoed the value: %v", bad.Name, err)
		}
	}
}
