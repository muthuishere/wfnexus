package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubApl is a fake `apl`: it records each call (argv + stdin) and answers
// with canned JSON. Nothing reaches Google, Microsoft or WhatsApp.
func stubApl(t *testing.T, dir string) (calls func() []map[string]any) {
	t.Helper()
	log := filepath.Join(dir, "apl-calls.jsonl")
	script := filepath.Join(dir, "apl")
	_ = os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
body = sys.stdin.read() if "--body-file" in args and args[args.index("--body-file") + 1] == "-" else ""
open(`+"\""+log+"\""+`, "a").write(json.dumps({"args": args, "body": body}) + "\n")
if "-o" in args:
    open(args[args.index("-o") + 1], "w").write("file-bytes")
if args[:1] == ["find"]:
    print(json.dumps([{"source": "gmail", "text": "invoice"}]))
elif args[:2] == ["contacts", "resolve"]:
    print(json.dumps({"name": args[2], "whatsapp": ["1555@s.whatsapp.net"]}))
elif "messages?" in " ".join(args):
    print(json.dumps({"value": [{"id": "m1", "subject": "hello"}], "messages": [{"id": "g1"}]}))
else:
    print(json.dumps({"id": "obj-1"}))
`), 0o755)
	t.Setenv("APL_BIN", script)
	return func() []map[string]any {
		raw, _ := os.ReadFile(log)
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var e map[string]any
			if json.Unmarshal([]byte(l), &e) == nil {
				out = append(out, e)
			}
		}
		return out
	}
}

func callCmd(t *testing.T, h http.Handler, name string, inputs map[string]any) (status, stdout string) {
	t.Helper()
	w := do(h, "POST", "/api/modules/"+name+"/call", "", map[string]any{"inputs": inputs, "wait": true})
	if w.Code != 201 {
		t.Fatalf("%s: %d %s", name, w.Code, w.Body)
	}
	var res struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"run"`
		Output struct {
			Stdout string `json:"stdout"`
		} `json:"output"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	// A write command pauses for approval first (engine); a person says yes.
	return approveOnce(t, h, res.Run.ID, res.Run.Status, res.Output.Stdout)
}

func argsOf(c map[string]any) string {
	var parts []string
	for _, a := range c["args"].([]any) {
		parts = append(parts, a.(string))
	}
	return strings.Join(parts, " ")
}

// Every apl command wrapper, through `wfx module call`, against a stub apl.
// Sends are policy-gated: no policy drafts and calls nothing; approval waits
// for a person; auto calls apl. The handle comes from the input, else the
// default profile's <kind>_handle.
func TestAplCommandWrappers(t *testing.T) {
	h, _, tmp := aplServer(t, nil)
	calls := stubApl(t, tmp)
	if w := do(h, "PUT", "/api/modules/apl/profiles/business", "", map[string]any{
		"account": "biz", "default": true,
		"inputs": map[string]string{"mail_handle": "google:me", "teams_handle": "ms:work", "calendar_handle": "google:me", "drive_handle": "ms:work"},
	}); w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	expect := func(n int, want ...string) {
		t.Helper()
		c := calls()
		if len(c) != n {
			t.Fatalf("want %d apl calls, got %d: %v", n, len(c), c)
		}
		got := argsOf(c[n-1])
		for _, w := range want {
			if !strings.Contains(got+" "+asString(c[n-1]["body"]), w) {
				t.Fatalf("last apl call %q (body %v) lacks %q", got, c[n-1]["body"], w)
			}
		}
	}

	// mail-send: no policy drafts and touches nothing.
	st, out := callCmd(t, h, "apl.mail-send", map[string]any{"to": "a@x.com", "subject": "Hi", "body": "hello"})
	if st != "done" || !strings.Contains(out, `"status": "drafted"`) || len(calls()) != 0 {
		t.Fatalf("mail-send draft: %s %s %v", st, out, calls())
	}
	// auto, handle from the profile (google) → Gmail send.
	st, out = callCmd(t, h, "apl.mail-send", map[string]any{"to": "a@x.com", "subject": "Hi", "body": "hello", "responsePolicy": "auto"})
	if st != "done" || !strings.Contains(out, `"status": "sent"`) {
		t.Fatalf("mail-send auto: %s %s", st, out)
	}
	expect(1, "call google:me POST https://gmail.googleapis.com/gmail/v1/users/me/messages/send", `"raw"`)
	// An explicit handle wins → Graph sendMail.
	st, _ = callCmd(t, h, "apl.mail-send", map[string]any{"to": "a@x.com", "subject": "Hi", "body": "hello", "responsePolicy": "auto", "handle": "ms:work"})
	if st != "done" {
		t.Fatalf("mail-send ms: %s", st)
	}
	expect(2, "call ms:work POST https://graph.microsoft.com/v1.0/me/sendMail", `"subject": "Hi"`)

	// mail-draft never sends; mail-list reads.
	if st, out = callCmd(t, h, "apl.mail-draft", map[string]any{"to": "a@x.com", "subject": "S", "body": "B"}); st != "done" || !strings.Contains(out, "drafted") {
		t.Fatalf("mail-draft: %s %s", st, out)
	}
	expect(3, "/drafts")
	if st, out = callCmd(t, h, "apl.mail-list", map[string]any{"query": "invoice"}); st != "done" || !strings.Contains(out, `"g1"`) {
		t.Fatalf("mail-list: %s %s", st, out)
	}
	expect(4, "GET https://gmail.googleapis.com/gmail/v1/users/me/messages?", "q=invoice")

	// teams-send auto (profile teams_handle ms:work).
	if st, _ = callCmd(t, h, "apl.teams-send", map[string]any{"chat": "19:abc", "text": "hey", "responsePolicy": "auto"}); st != "done" {
		t.Fatalf("teams-send: %s", st)
	}
	expect(5, "call ms:work POST https://graph.microsoft.com/v1.0/chats/19%3Aabc/messages", `"content": "hey"`)

	// calendar-list reads; calendar-create with approval waits for a person.
	if st, _ = callCmd(t, h, "apl.calendar-list", map[string]any{"days": "2"}); st != "done" {
		t.Fatalf("calendar-list: %s", st)
	}
	expect(6, "GET https://www.googleapis.com/calendar/v3/calendars/primary/events?")
	if st = callPaused(t, h, "apl.calendar-create", map[string]any{"subject": "Sync", "start": "2026-11-09T10:00:00Z", "end": "2026-11-09T10:30:00Z", "responsePolicy": "approval"}); st != "awaiting_approval" {
		t.Fatalf("calendar-create approval: %s", st)
	}
	expect(6) // nothing created before a person says yes

	// drive-get writes the file; drive-put auto uploads it.
	dl := filepath.Join(tmp, "dl.txt")
	if st, _ = callCmd(t, h, "apl.drive-get", map[string]any{"path": "docs/a.txt", "out": dl}); st != "done" {
		t.Fatalf("drive-get: %s", st)
	}
	expect(7, "call ms:work GET https://graph.microsoft.com/v1.0/me/drive/root:/docs/a.txt:/content -o "+dl)
	if st, _ = callCmd(t, h, "apl.drive-put", map[string]any{"path": "docs/b.txt", "file": dl, "responsePolicy": "auto"}); st != "done" {
		t.Fatalf("drive-put: %s", st)
	}
	expect(8, "PUT https://graph.microsoft.com/v1.0/me/drive/root:/docs/b.txt:/content --body-file "+dl)

	// contacts-find and find.
	if st, out = callCmd(t, h, "apl.contacts-find", map[string]any{"name": "kamalesh"}); st != "done" || !strings.Contains(out, "1555@s.whatsapp.net") {
		t.Fatalf("contacts-find: %s %s", st, out)
	}
	expect(9, "contacts resolve kamalesh")
	if st, out = callCmd(t, h, "apl.find", map[string]any{"query": "invoice", "only": "gmail"}); st != "done" || !strings.Contains(out, "invoice") {
		t.Fatalf("find: %s %s", st, out)
	}
	expect(10, "find invoice --only gmail --limit 10 --json")

	// A missing required input is refused before anything runs.
	w := do(h, "POST", "/api/modules/apl.teams-send/call", "", map[string]any{"inputs": map[string]any{"text": "x"}})
	if w.Code != 400 {
		t.Fatalf("missing chat: %d %s", w.Code, w.Body)
	}
}

func asString(v any) string { s, _ := v.(string); return s }
