package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/proposal"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Git-native workflows. A save or delete on a project whose workflows
// directory is a git repository does not write the tracked checkout: it opens
// a PROPOSAL (internal/proposal), records it here, asks the classifier for its
// advice, and waits for a person. A plain directory keeps the direct write.

// gitMu serialises every git operation the API makes. Worktree creation and a
// merge into the same checkout must not interleave; the volume is one person
// clicking Save, so one lock is the right amount of machinery.
var gitMu sync.Mutex

// autoApprover is who a proposal says approved it when the threshold did.
const autoApprover = "wfx-classifier"

// proposalActor reads who is asking, the same way a run's resolve does: the
// authenticated subject wins, a body/header claim is the loopback fallback.
func proposalActor(r *http.Request, bodyActor string) engine.Actor {
	if bodyActor == "" {
		bodyActor = r.Header.Get("X-WFX-Actor")
	}
	return actorOf(r, stepBody{Actor: bodyActor})
}

// openProposal is the shared path for a save, a delete and a drift: open the
// branch, record it, review it, and auto-approve when configured to.
func (s *Server) openProposal(ctx context.Context, t engine.WorkflowTarget, kind, msg string,
	apply func(string) error, validation string, by engine.Actor) (*store.Proposal, error) {
	gitMu.Lock()
	opened, err := t.Repo.Open(ctx, proposal.Change{
		Workflow: t.Name, Kind: kind, Message: msg,
		Body:    fmt.Sprintf("%s\n\nProposed by %s from wfnexus. Validation: %s.", msg, orUnknown(by.ID), validation),
		WorkDir: s.eng.ProposalWorkDir(), Apply: apply,
	})
	var diff string
	if err == nil {
		diff, _ = t.Repo.Diff(ctx, opened.Base, opened.Branch)
	}
	gitMu.Unlock()
	if err != nil {
		return nil, err
	}
	p := &store.Proposal{
		Project: t.Project, Workflow: t.Name, Kind: kind,
		Branch: opened.Branch, Base: opened.Base, Commit: opened.Commit, PRURL: opened.PRURL,
		Note: opened.Note, CreatedBy: by.ID,
	}
	rev := s.eng.ReviewProposal(ctx, diff, validation)
	p.ReviewVerdict, p.ReviewReason, p.ReviewScore = rev.Verdict, rev.Reason, rev.Score
	if err := s.store.CreateProposal(ctx, p); err != nil {
		return nil, err
	}
	if th := s.eng.ProposalAutoApprove(); th > 0 && rev.Verdict == "approve" && rev.Score != nil && *rev.Score >= th {
		if err := s.approveProposal(ctx, p, autoApprover); err != nil {
			// Auto-approve is a convenience; its failure leaves a pending
			// proposal for a person, never a lost change.
			p.Note = "auto-approve failed: " + err.Error()
		}
		if fresh, err := s.store.GetProposal(ctx, p.ID); err == nil {
			p = fresh
		}
	}
	return p, nil
}

func fileOrDir(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func orUnknown(s string) string {
	if s == "" {
		return "an unnamed actor"
	}
	return s
}

// proposeSave is saveWorkflow's git branch. ok=false means the target is not
// git-backed and the caller writes directly.
func (s *Server) proposeSave(w http.ResponseWriter, r *http.Request, project string, def *workflow.Definition) bool {
	t, err := s.eng.TargetForSave(r.Context(), project, def.Name)
	if err != nil || !t.Git {
		return false
	}
	// Validate BEFORE proposing, exactly as a direct save does: an illegal
	// definition is a 400 now, never a branch somebody has to reject.
	if err := s.eng.CheckSave(def); err != nil {
		writeErr(w, 400, err)
		return true
	}
	kind := proposal.KindCreate
	if existsIn(t) {
		kind = proposal.KindEdit
	}
	msg := fmt.Sprintf("wfx: %s workflow %s", kind, def.Name)
	p, err := s.openProposal(r.Context(), t, kind, msg, func(dir string) error {
		_, err := s.eng.WriteWorkflow(dir, def)
		return err
	}, "ok", proposalActor(r, ""))
	if errors.Is(err, proposal.ErrNoChange) {
		writeJSON(w, 200, map[string]any{"ok": true, "unchanged": true, "definition": s.eng.Definitions()[def.Name]})
		return true
	}
	if err != nil {
		writeErr(w, 500, err)
		return true
	}
	writeJSON(w, 202, map[string]any{"ok": true, "proposal": p})
	return true
}

// existsIn reports whether the project already has this workflow committed or
// on disk, which is what makes a save an edit rather than a create.
func existsIn(t engine.WorkflowTarget) bool {
	for _, p := range []string{t.Name, t.Name + ".yaml", t.Name + ".yml"} {
		if fileOrDir(t.Dir + "/" + p) {
			return true
		}
	}
	return false
}

// proposeDelete is deleteWorkflow's git branch.
func (s *Server) proposeDelete(w http.ResponseWriter, r *http.Request, name string) bool {
	t, err := s.eng.TargetForDelete(r.Context(), name)
	if err != nil || !t.Git {
		return false
	}
	msg := fmt.Sprintf("wfx: delete workflow %s", t.Name)
	p, err := s.openProposal(r.Context(), t, proposal.KindDelete, msg, func(dir string) error {
		return workflow.Delete(dir, t.Name)
	}, "ok (delete)", proposalActor(r, ""))
	if err != nil {
		writeErr(w, 400, err)
		return true
	}
	writeJSON(w, 202, map[string]any{"ok": true, "proposal": p})
	return true
}

// ---- routes ----

func (s *Server) proposalRoutes(r chi.Router) {
	r.Get("/proposals", s.listProposals)
	r.Get("/proposals/drift", s.listDrift)
	r.Post("/proposals/drift", s.proposeDrift)
	r.Get("/proposals/{id}", s.getProposal)
	r.Post("/proposals/{id}/approve", s.approveProposalRoute)
	r.Post("/proposals/{id}/reject", s.rejectProposalRoute)
}

// GET /api/proposals?project=&workflow=&status=
func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := q.Get("project")
	if scope := s.scopeOf(r); scope != "" {
		if project != "" && project != scope {
			writeJSON(w, 200, []*store.Proposal{})
			return
		}
		project = scope
	}
	rows, err := s.store.ListProposals(r.Context(), store.ProposalFilter{
		Project: project, Workflow: q.Get("workflow"), Status: q.Get("status")})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) proposalFor(w http.ResponseWriter, r *http.Request) (*store.Proposal, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 400, errors.New("bad proposal id"))
		return nil, false
	}
	p, err := s.store.GetProposal(r.Context(), id)
	if err != nil || !s.inScope(r, p.Project) {
		notFound(w)
		return nil, false
	}
	return p, true
}

// GET /api/proposals/{id} → {proposal, diff}
func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := s.proposalFor(w, r)
	if !ok {
		return
	}
	diff := ""
	if t, err := s.eng.TargetForProject(r.Context(), p.Project, p.Workflow); err == nil && t.Git {
		gitMu.Lock()
		if p.Status == proposal.StatusRejected || p.Status == proposal.StatusMerged {
			diff, _ = t.Repo.CommitDiff(r.Context(), p.Commit)
		} else {
			diff, _ = t.Repo.Diff(r.Context(), p.Base, p.Branch)
		}
		gitMu.Unlock()
	}
	writeJSON(w, 200, map[string]any{"proposal": p, "diff": diff})
}

type decideBody struct {
	Actor         string `json:"actor"`
	ActorInferred bool   `json:"actorInferred"`
	Reason        string `json:"reason"`
}

func decodeDecide(r *http.Request) decideBody {
	var b decideBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b)
	}
	if b.Actor == "" {
		b.Actor = r.Header.Get("X-WFX-Actor")
	}
	return b
}

// POST /api/proposals/{id}/approve {actor}
func (s *Server) approveProposalRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.proposalFor(w, r)
	if !ok {
		return
	}
	b := decodeDecide(r)
	by := actorOf(r, stepBody{Actor: b.Actor, ActorInferred: b.ActorInferred})
	if !requireActor(w, by) {
		return
	}
	if err := s.approveProposal(r.Context(), p, by.ID); err != nil {
		code := 400
		if errors.Is(err, store.ErrProposalDecided) {
			code = 409
		}
		writeErr(w, code, err)
		return
	}
	fresh, _ := s.store.GetProposal(r.Context(), p.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "proposal": fresh})
}

// approveProposal merges the branch (gh when there is a PR, locally
// otherwise), then reloads the catalog from the updated checkout.
func (s *Server) approveProposal(ctx context.Context, p *store.Proposal, by string) error {
	if p.Status != proposal.StatusPending && p.Status != proposal.StatusApproved {
		return store.ErrProposalDecided
	}
	t, err := s.eng.TargetForProject(ctx, p.Project, p.Workflow)
	if err != nil {
		return err
	}
	if !t.Git {
		return fmt.Errorf("project %q is no longer a git repository", p.Project)
	}
	gitMu.Lock()
	err = t.Repo.Merge(ctx, p.Base, p.Branch, p.PRURL)
	gitMu.Unlock()
	if err != nil {
		// The person said yes; git said no. Record the yes, keep it retryable.
		_ = s.store.DecideProposal(ctx, p.ID, proposal.StatusApproved, by, "", "merge failed: "+err.Error())
		return fmt.Errorf("approved, but the merge failed: %w", err)
	}
	if err := s.store.DecideProposal(ctx, p.ID, proposal.StatusMerged, by, "", p.Note); err != nil {
		return err
	}
	return s.eng.ReloadDefinitions()
}

// POST /api/proposals/{id}/reject {actor, reason}
func (s *Server) rejectProposalRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.proposalFor(w, r)
	if !ok {
		return
	}
	b := decodeDecide(r)
	by := actorOf(r, stepBody{Actor: b.Actor, ActorInferred: b.ActorInferred})
	if !requireActor(w, by) {
		return
	}
	if p.Status != proposal.StatusPending && p.Status != proposal.StatusApproved {
		writeErr(w, 409, store.ErrProposalDecided)
		return
	}
	if t, err := s.eng.TargetForProject(r.Context(), p.Project, p.Workflow); err == nil && t.Git {
		gitMu.Lock()
		err = t.Repo.Close(r.Context(), p.Branch, p.PRURL, b.Reason)
		gitMu.Unlock()
		if err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	if err := s.store.DecideProposal(r.Context(), p.ID, proposal.StatusRejected, by.ID, b.Reason, p.Note); err != nil {
		writeErr(w, 409, err)
		return
	}
	fresh, _ := s.store.GetProposal(r.Context(), p.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "proposal": fresh})
}

// GET /api/proposals/drift?project= → [{project, workflow, kind, files}]
//
// Workflow folders in a project's .wfx/workflows that are uncommitted or
// untracked: somebody edited by hand, or an agent wrote one.
func (s *Server) listDrift(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("project")
	type row struct {
		Project string `json:"project"`
		proposal.Drift
	}
	out := []row{}
	for _, src := range s.eng.Sources() {
		if want != "" && src.Name != want {
			continue
		}
		if !s.inScope(r, src.Name) {
			continue
		}
		t, err := s.eng.TargetForProject(r.Context(), src.Name, "")
		if err != nil || !t.Git {
			continue
		}
		gitMu.Lock()
		ds, err := t.Repo.Drift(r.Context())
		gitMu.Unlock()
		if err != nil {
			continue
		}
		for _, d := range ds {
			out = append(out, row{Project: src.Name, Drift: d})
		}
	}
	writeJSON(w, 200, out)
}

// POST /api/proposals/drift {project, workflow, actor} → 202 {proposal}
//
// Turns one drifted workflow into a proposal carrying exactly what is on disk.
// A drifted definition that does not load is still proposed — it is already
// on disk — but its validation failure is what the classifier and the person
// see.
func (s *Server) proposeDrift(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Project  string `json:"project"`
		Workflow string `json:"workflow"`
		Actor    string `json:"actor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Project == "" || b.Workflow == "" {
		writeErr(w, 400, errors.New("body needs `project` and `workflow`"))
		return
	}
	if !s.inScope(r, b.Project) {
		notFound(w)
		return
	}
	t, err := s.eng.TargetForProject(r.Context(), b.Project, b.Workflow)
	if err != nil {
		notFound(w)
		return
	}
	if !t.Git {
		writeErr(w, 400, fmt.Errorf("project %q is not a git repository", b.Project))
		return
	}
	gitMu.Lock()
	ds, err := t.Repo.Drift(r.Context())
	gitMu.Unlock()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var found *proposal.Drift
	for i := range ds {
		if ds[i].Workflow == b.Workflow {
			found = &ds[i]
		}
	}
	if found == nil {
		writeErr(w, 404, fmt.Errorf("workflow %q has no uncommitted changes in %q", b.Workflow, b.Project))
		return
	}
	validation := "ok"
	if found.Kind != proposal.KindDelete {
		if defs, err := workflow.LoadDir(t.Dir, s.eng.Validator()); err != nil {
			validation = "does not load: " + err.Error()
		} else if d := defs[b.Workflow]; d == nil {
			validation = "does not load under its name"
		} else if err := s.eng.CheckSave(d); err != nil {
			validation = "invalid: " + err.Error()
		}
	}
	msg := fmt.Sprintf("wfx: %s workflow %s (from uncommitted changes)", found.Kind, b.Workflow)
	p, err := s.openProposal(r.Context(), t, found.Kind, msg, t.Repo.CopyDrift(*found), validation, proposalActor(r, b.Actor))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "proposal": p, "validation": validation})
}
