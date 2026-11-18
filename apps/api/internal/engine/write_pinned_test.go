package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// moduleDoor stands in for the API's module door (internal/api/modulecalls.go)
// at the engine's self URL: /profile answers with profile, and /outbox queues
// through the engine and answers a queued item as sent, recording the request.
func moduleDoor(t *testing.T, h *harness, module string, profile map[string]any) *[]map[string]any {
	t.Helper()
	var outbox []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/module-self/"+module+"/profile":
			_ = json.NewEncoder(w).Encode(profile)
		case r.Method == "POST" && r.URL.Path == "/api/module-self/"+module+"/outbox":
			var body struct {
				Service, Profile, Account, ID string
				Request                       json.RawMessage
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			it, err := h.eng.Enqueue(r.Context(), module, body.Service, body.Profile, body.Account, body.ID, body.Request)
			if err != nil {
				http.Error(w, `{"error":`+string(mustJSON(err.Error()))+`}`, http.StatusConflict)
				return
			}
			outbox = append(outbox, map[string]any{"profile": body.Profile, "account": body.Account, "service": it.Service})
			it.Status, it.Result = "sent", json.RawMessage(`{"id":"wamid-1"}`)
			_ = json.NewEncoder(w).Encode(it)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	h.eng.cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
	return &outbox
}

// apl's mail, Teams, calendar and Drive writes act on the handle of the
// profile pinned at the pause, never on a profile looked up while they run —
// here the module door would hand out another handle if asked.
func TestAplWritesUseThePinnedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	file := filepath.Join(t.TempDir(), "up.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	for cmd, with := range map[string]string{
		"mail-send":       `{ to: a@b.example, subject: s, body: b, responsePolicy: auto }`,
		"teams-send":      `{ chat: c1, text: t, responsePolicy: auto }`,
		"calendar-create": `{ subject: s, start: "2026-01-01T10:00:00", end: "2026-01-01T11:00:00", responsePolicy: auto }`,
		"drive-put":       fmt.Sprintf(`{ path: /up.txt, file: %q, responsePolicy: auto }`, file),
	} {
		t.Run(cmd, func(t *testing.T) {
			calls := ghStub(t)
			bin := filepath.Dir(calls)
			if err := os.WriteFile(filepath.Join(bin, "apl"), []byte(fmt.Sprintf("#!/bin/sh\necho \"apl $*\" >> %s\necho '{}'\n", calls)), 0o755); err != nil {
				t.Fatal(err)
			}
			defs := loadWithModules(t, map[string]string{
				"w.yaml": "name: w\nsteps:\n  - id: w\n    uses: apl." + cmd + "@0\n    with: " + with + "\n",
			})
			h := newHarness(t, defs["w"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			ctx := context.Background()
			handles := func(h string) map[string]string {
				return map[string]string{"mail_handle": h, "teams_handle": h, "calendar_handle": h, "drive_handle": h}
			}
			moduleDoor(t, h, "apl", map[string]any{"name": "work", "account": "elsewhere", "inputs": handles("ms:elsewhere")})
			if _, err := h.eng.SetProfile(ctx, "apl", Profile{Name: "work", Account: "acct", Inputs: handles("ms:pinned"), Default: true}); err != nil {
				t.Fatal(err)
			}
			run := waitSettled(t, h, h.run(nil).ID)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s (%s)", run.Status, run.Error)
			}
			if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
				t.Fatal(err)
			}
			run = waitSettled(t, h, run.ID)
			b, _ := os.ReadFile(calls)
			if run.Status != "done" || !strings.Contains(string(b), "ms:pinned") || strings.Contains(string(b), "ms:elsewhere") {
				t.Fatalf("run = %s (%s); apl: %s — want the pinned handle ms:pinned", run.Status, run.Error, b)
			}
		})
	}
}

// whatsapp-send queues under the profile NAME pinned at the pause: the
// module's default moving to another profile (same account) at the send step
// does not move the send.
func TestWhatsappSendQueuesUnderThePinnedProfileName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: tell\n    uses: apl.whatsapp-send@0\n    with: { chat: ops@g.us, text: deployed, responsePolicy: auto }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	outbox := moduleDoor(t, h, "apl", nil)
	if err := h.store.PutState(ctx, serviceScope, "apl.whatsapp/biz", "spec", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.SetProfile(ctx, "apl", Profile{Name: "business", Account: "biz", Default: true}); err != nil {
		t.Fatal(err)
	}
	run := waitSettled(t, h, h.run(nil).ID)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	h.eng.testHookBeforeCallStep = func(step *workflow.Step, _ string) {
		if step.ID == "tell.send" {
			if _, err := h.eng.SetProfile(ctx, "apl", Profile{Name: "personal", Account: "biz", Default: true}); err != nil {
				t.Error(err)
			}
		}
	}
	if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = waitSettled(t, h, run.ID)
	if run.Status != "done" || len(*outbox) != 1 || (*outbox)[0]["profile"] != "business" || (*outbox)[0]["account"] != "biz" {
		t.Fatalf("run = %s (%s); outbox %v — want one send under profile business, account biz", run.Status, run.Error, *outbox)
	}
}

// The two places a retry of a write step is sent back to its call's first
// step, each on its own: Retry reports the run as queued at the call's first
// step, and resetFrom resets from it, whoever calls it.
func TestARetryOfAWriteStepRestartsFromTheFirstStepOnBothPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n  - id: later\n    needs: [open]\n    requires_approval: true\n    run: echo later\n",
	})
	def := defs["w"]
	ctx := context.Background()
	parked := func(t *testing.T) (*harness, uuid.UUID) {
		h := newHarness(t, def, newFakeLLM(t), "")
		h.eng.cfg.ModulesDir = repoPath(t, "modules")
		run := waitSettled(t, h, h.run(nil).ID)
		if err := h.eng.Approve(ctx, run.ID, "open.plan", Actor{ID: "alice", Via: "test"}); err != nil {
			t.Fatal(err)
		}
		run = waitSettled(t, h, run.ID)
		if run.Status != "awaiting_approval" || run.CurrentStep != "later" {
			t.Fatalf("run = %s at %s (%s)", run.Status, run.CurrentStep, run.Error)
		}
		return h, run.ID
	}

	t.Run("resetFrom", func(t *testing.T) {
		h, r := parked(t)
		if err := h.eng.resetFrom(ctx, r, def, "open.act"); err != nil {
			t.Fatal(err)
		}
		steps := h.steps(r)
		if st := steps["open.plan"]; st.Status != "pending" {
			t.Fatalf("after a reset from open.act the call's first step is %s, want pending", st.Status)
		}
	})

	t.Run("Retry", func(t *testing.T) {
		h, r := parked(t)
		// Hold the run off (no slot) so what Retry itself recorded can be read.
		held := 0
		for len(h.eng.slots) < cap(h.eng.slots) {
			h.eng.slots <- struct{}{}
			held++
		}
		if err := h.eng.Retry(ctx, r, "open.act"); err != nil {
			t.Fatal(err)
		}
		run, _ := h.store.GetRun(ctx, r)
		for ; held > 0; held-- {
			<-h.eng.slots
		}
		if run.Status != "queued" || run.CurrentStep != "open.plan" {
			t.Fatalf("Retry recorded %s at %s, want queued at the call's first step open.plan", run.Status, run.CurrentStep)
		}
		if run = waitSettled(t, h, r); run.Status != "awaiting_approval" || run.CurrentStep != "open.plan" {
			t.Fatalf("after the retry the run = %s at %s, want a new pause at open.plan", run.Status, run.CurrentStep)
		}
	})
}
