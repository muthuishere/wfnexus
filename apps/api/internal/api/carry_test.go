package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCarry puts a carry stand-in on CARRY_BIN that records its argv and stdin
// and answers carry's JSON envelope, so the server's real exec path runs.
func fakeCarry(t *testing.T) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "ARGS $*" >> "` + logFile + `"
in=$(cat)
[ -n "$in" ] && printf 'STDIN %s\n' "$in" >> "` + logFile + `"
case "$2 $3" in
  "tool ls")   echo '{"ok":true,"result":{"tools":[{"name":"jq","versions":["1.7.1"],"channels":{"stable":"1.7.1"}}]}}' ;;
  "tool add")  echo '{"ok":true,"result":{"state":"'$4'","version":2,"tool":"'$5'"}}' ;;
  "env get")   echo '{"ok":true,"result":{"state":"'$4'","env":{"LOG_LEVEL":"debug","A":"1"}}}' ;;
  "get "*)     echo '{"ok":true,"result":{"path":"x","size":5,"mode":420,"content_base64":"aGVsbG8="}}' ;;
  "rollback "*) echo '{"ok":false,"error":{"code":"not_found","message":"no version 99"}}'; exit 2 ;;
  *)           echo '{"ok":true,"result":{"state":"wfnexus","version":3}}' ;;
esac
`
	bin := filepath.Join(dir, "carry")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARRY_BIN", bin)
	t.Setenv("CARRY_REPO", "s3://test/carry")
	t.Setenv("CARRY_STATE", "")
	return logFile
}

func TestCarryNotConfiguredIsAClear409(t *testing.T) {
	t.Setenv("CARRY_REPO", "")
	h, _ := testServer(t, "127.0.0.1:8090")
	res := do(h, "GET", "/api/carry", "", nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"configured":false`) {
		t.Fatalf("status: %d %s", res.Code, res.Body)
	}
	res = do(h, "POST", "/api/carry/tools", "", map[string]any{"tool": "jq@1.7.1"})
	if res.Code != 409 || !strings.Contains(res.Body.String(), "CARRY_REPO") {
		t.Fatalf("want 409 naming CARRY_REPO, got %d %s", res.Code, res.Body)
	}
}

func TestCarryDrivesTheBinary(t *testing.T) {
	logFile := fakeCarry(t)
	h, _ := testServer(t, "127.0.0.1:8090")

	if res := do(h, "POST", "/api/carry/tools", "", map[string]any{"tool": "jq@1.7.1", "state": "worker-1"}); res.Code != 200 {
		t.Fatalf("tool add: %d %s", res.Code, res.Body)
	}
	if res := do(h, "GET", "/api/carry/tools", "", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "jq") {
		t.Fatalf("tool ls: %d %s", res.Code, res.Body)
	}
	content := base64.StdEncoding.EncodeToString([]byte("[user]\n"))
	if res := do(h, "PUT", "/api/carry/file", "", map[string]any{"path": "/home/wfx/.gitconfig", "content_base64": content}); res.Code != 200 {
		t.Fatalf("put: %d %s", res.Code, res.Body)
	}
	if res := do(h, "PUT", "/api/carry/file", "", map[string]any{"path": "relative", "content_base64": content}); res.Code != 400 {
		t.Fatalf("relative path must be refused: %d", res.Code)
	}
	res := do(h, "GET", "/api/carry/file?path=/etc/x", "", nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "aGVsbG8=") {
		t.Fatalf("get: %d %s", res.Code, res.Body)
	}
	// A listing of env names never carries a value.
	res = do(h, "GET", "/api/carry/env", "", nil)
	if res.Code != 200 || strings.Contains(res.Body.String(), "debug") || !strings.Contains(res.Body.String(), "LOG_LEVEL") {
		t.Fatalf("env listing: %d %s", res.Code, res.Body)
	}
	if res := do(h, "PUT", "/api/carry/env", "", map[string]any{"vars": map[string]string{"LOG_LEVEL": "info"}}); res.Code != 200 {
		t.Fatalf("env set: %d %s", res.Code, res.Body)
	}
	if res := do(h, "POST", "/api/carry/rollback", "", map[string]any{"version": 99}); res.Code != 404 {
		t.Fatalf("carry's not_found should be a 404, got %d %s", res.Code, res.Body)
	}

	raw, _ := os.ReadFile(logFile)
	calls := string(raw)
	for _, want := range []string{
		"ARGS --json tool add worker-1 jq@1.7.1",
		"ARGS --json put wfnexus:/home/wfx/.gitconfig -",
		"ARGS --json env import wfnexus -",
		"ARGS --json rollback wfnexus 99",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("carry was not called with %q; calls:\n%s", want, calls)
		}
	}
	// The env value went over stdin, never argv.
	if strings.Contains(calls, "ARGS --json env import wfnexus - LOG_LEVEL") || !strings.Contains(calls, "STDIN LOG_LEVEL=info") {
		t.Errorf("env value must travel on stdin; calls:\n%s", calls)
	}
}

func TestCarryNeedsAdminOffLoopback(t *testing.T) {
	if got := PermissionFor("POST", "/api/carry/tools"); got != PermAdminWrite {
		t.Fatalf("carry write permission = %q", got)
	}
	if got := PermissionFor("GET", "/api/carry/env"); got != PermAdminRead {
		t.Fatalf("carry read permission = %q", got)
	}
}
