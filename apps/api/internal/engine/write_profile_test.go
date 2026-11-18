package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The write step uses the profile pinned at the pause even when the profile
// changes after that step's own check, just before the module reads it: the
// module never looks the profile up for itself.
func TestAWriteStepUsesThePinnedProfileWhenItChangesAtTheStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, responsePolicy: auto }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	if _, err := h.eng.SetProfile(ctx, "gh", Profile{Name: "work", Account: "me", Inputs: map[string]string{"repo": "o/approved"}, Default: true}); err != nil {
		t.Fatal(err)
	}
	run := waitSettled(t, h, h.run(nil).ID)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	// At the write step, after its check and before the module runs.
	h.eng.testHookBeforeCallStep = func(step *workflow.Step, _ string) {
		if step.ID == "open.act" {
			if _, err := h.eng.SetProfile(ctx, "gh", Profile{Name: "work", Account: "me", Inputs: map[string]string{"repo": "o/elsewhere"}, Default: true}); err != nil {
				t.Error(err)
			}
		}
	}
	if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = waitSettled(t, h, run.ID)
	b, _ := os.ReadFile(calls)
	if run.Status != "done" || !strings.Contains(string(b), "-R o/approved") || strings.Contains(string(b), "o/elsewhere") {
		t.Fatalf("run = %s (%s); gh: %s — want the pinned repo o/approved", run.Status, run.Error, b)
	}
}

// apl whatsapp-send hands the outbox the account it was approved for, and the
// outbox refuses the send when the profile names another account by the time
// it is queued (changed after the step's check, before the send).
func TestTheOutboxRefusesASendWhoseAccountChangedAfterApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "account changed"}[changed], func(t *testing.T) {
			defs := loadWithModules(t, map[string]string{
				"w.yaml": "name: w\nsteps:\n  - id: tell\n    uses: apl.whatsapp-send@0\n    with: { chat: ops@g.us, text: deployed, responsePolicy: auto }\n",
			})
			h := newHarness(t, defs["w"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			ctx := context.Background()
			// The module door the step talks to: the outbox, as the API serves
			// it (internal/api/modulecalls.go), answering a queued item as sent.
			var queued []OutboxItem
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/module-self/apl/outbox" {
					http.NotFound(w, r)
					return
				}
				var body struct {
					Service, Profile, Account, ID string
					Request                       json.RawMessage
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				it, err := h.eng.Enqueue(r.Context(), "apl", body.Service, body.Profile, body.Account, body.ID, body.Request)
				if err != nil {
					http.Error(w, `{"error":`+string(mustJSON(err.Error()))+`}`, http.StatusConflict)
					return
				}
				queued = append(queued, it)
				it.Status, it.Result = "sent", json.RawMessage(`{"id":"wamid-1"}`)
				_ = json.NewEncoder(w).Encode(it)
			}))
			defer srv.Close()
			h.eng.cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
			for _, acct := range []string{"biz", "other"} {
				if err := h.store.PutState(ctx, serviceScope, "apl.whatsapp/"+acct, "spec", "{}"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.eng.SetProfile(ctx, "apl", Profile{Name: "business", Account: "biz", Default: true}); err != nil {
				t.Fatal(err)
			}
			run := waitSettled(t, h, h.run(nil).ID)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s (%s)", run.Status, run.Error)
			}
			if changed {
				// After the send step's check, before it queues the send.
				h.eng.testHookBeforeCallStep = func(step *workflow.Step, _ string) {
					if step.ID == "tell.send" {
						if _, err := h.eng.SetProfile(ctx, "apl", Profile{Name: "business", Account: "other", Default: true}); err != nil {
							t.Error(err)
						}
					}
				}
			}
			if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
				t.Fatal(err)
			}
			run = waitSettled(t, h, run.ID)
			if !changed {
				if run.Status != "done" || len(queued) != 1 || queued[0].Service != "apl.whatsapp/biz" {
					t.Fatalf("run = %s (%s); queued %+v", run.Status, run.Error, queued)
				}
				return
			}
			if run.Status != "failed" || len(queued) != 0 {
				t.Fatalf("run = %s (%s); queued %+v — want failed with nothing queued", run.Status, run.Error, queued)
			}
			steps := h.steps(run.ID)
			if out := string(steps["tell.send"].Output); !strings.Contains(out, "approved for") {
				t.Fatalf("the send step does not say why: %s", out)
			}
		})
	}
}
