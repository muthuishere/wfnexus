package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// `wfx send` goes through the server's API — the same path a send: step takes
// — with the flags kept out of the text and a local file carried in the body.
func TestSendPostsToTheChannelsAPI(t *testing.T) {
	tempContexts(t)
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"channel":"ops","id":"job-1"}`))
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)
	t.Setenv("WFX_API_TOKEN", "")

	f := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sendCmd([]string{"--channel", "ops", "deploy", "--file", f, "done", "--as", "ada"}); err != nil {
		t.Fatal(err)
	}
	if path != "POST /api/channels/send" {
		t.Fatalf("hit %s", path)
	}
	if body["channel"] != "ops" || body["text"] != "deploy done" || body["actor"] != "ada" {
		t.Fatalf("body %v", body)
	}
	file, _ := body["file"].(map[string]any)
	if file["name"] != "report.txt" || file["data"] != base64.StdEncoding.EncodeToString([]byte("hello")) {
		t.Fatalf("file %v", file)
	}
	if err := sendCmd(nil); err == nil {
		t.Fatal("an empty send must be refused before it reaches the server")
	}
}
