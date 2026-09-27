package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/proposal"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The engine's half of git-native workflows: WHERE a change goes, whether it
// is legal, and what the classifier thinks of it. The git mechanics are
// internal/proposal; persistence and the decision are the API's.

// WorkflowTarget is where a save or delete of a workflow lands.
type WorkflowTarget struct {
	Project string
	Name    string // the short name, as the file is called
	Dir     string // the project's workflows directory
	Repo    proposal.Repo
	Git     bool // Dir is inside a git work tree with a commit
}

// TargetForSave resolves the directory a save of this name writes into (the
// same rule as SaveWorkflowIn) and whether it is git-backed.
func (e *Engine) TargetForSave(ctx context.Context, project, name string) (WorkflowTarget, error) {
	dir, err := e.saveDir(project, name)
	if err != nil {
		return WorkflowTarget{}, err
	}
	if project == "" {
		project = e.ProjectFor(name)
	}
	t := WorkflowTarget{Project: project, Name: name, Dir: dir}
	t.Repo, t.Git = proposal.Detect(ctx, dir)
	return t, nil
}

// TargetForDelete resolves an existing workflow (plain or qualified name) to
// the project and directory it was loaded from.
func (e *Engine) TargetForDelete(ctx context.Context, name string) (WorkflowTarget, error) {
	def := e.Definitions()[name]
	if def == nil {
		return WorkflowTarget{}, fmt.Errorf("workflow %q not found", name)
	}
	src := def.Source
	if src == "" {
		src = "local"
	}
	return e.TargetForProject(ctx, src, def.Name)
}

// TargetForProject resolves a project's workflows directory directly.
func (e *Engine) TargetForProject(ctx context.Context, project, name string) (WorkflowTarget, error) {
	for _, s := range e.Sources() {
		if s.Name == project && s.Name != templatesSource {
			t := WorkflowTarget{Project: project, Name: name, Dir: s.Dir}
			t.Repo, t.Git = proposal.Detect(ctx, s.Dir)
			return t, nil
		}
	}
	return WorkflowTarget{}, fmt.Errorf("no project named %q", project)
}

// CheckSave is every refusal a save would make, without the write: the
// definition, its schemas, and the host half of its mounts.
func (e *Engine) CheckSave(d *workflow.Definition) error {
	if err := e.checkMountHosts(d.Mount); err != nil {
		return err
	}
	return e.CheckWorkflow(d)
}

// WriteWorkflow writes an already-checked definition into dir (a proposal
// worktree), without reloading — the tracked catalog has not changed.
func (e *Engine) WriteWorkflow(dir string, d *workflow.Definition) (string, error) {
	return workflow.Save(dir, d, e.validator())
}

// Validator is the catalog a definition is validated against.
func (e *Engine) Validator() workflow.Catalog { return e.validator() }

// ProposalWorkDir is where proposal worktrees are cut. They live only as long
// as one commit takes.
func (e *Engine) ProposalWorkDir() string {
	if e.cfg.WorkDir == "" {
		return filepath.Join(os.TempDir(), "wfx-proposals")
	}
	return filepath.Join(e.cfg.WorkDir, "proposals")
}

// Review is the classifier's advice on a proposal.
type Review struct {
	Verdict string   `json:"verdict"` // approve|reject|unreviewed
	Reason  string   `json:"reason"`
	Score   *float64 `json:"score,omitempty"`
}

// ProposalAutoApprove is the configured auto-approve threshold; 0 is off.
func (e *Engine) ProposalAutoApprove() float64 { return e.cfg.ProposalAutoApprove }

// ReviewUnreviewed is the answer when there is nobody to ask.
func ReviewUnreviewed(why string) Review { return Review{Verdict: "unreviewed", Reason: why} }

// classifierConfigured says whether a review can be asked for at all: a test
// override, or the default endpoint with its key present. The key is looked
// up by NAME and its value never read beyond "is it set".
func (e *Engine) classifierConfigured() bool {
	if e.classifierOpts != nil {
		return true
	}
	if e.cfg.ClassifierBaseURL == "" || e.cfg.ClassifierModel == "" {
		return false
	}
	return e.cfg.ClassifierAPIKeyEnv == "" || os.Getenv(e.cfg.ClassifierAPIKeyEnv) != ""
}

// proposalQuestion is the one question asked of every proposal.
const proposalQuestion = "safe_to_merge"

func proposalState(diff, validation string) string {
	return "A change to a wfnexus workflow definition, proposed from the builder.\n\n" +
		"Validation: " + validation + "\n\nDiff:\n" + diff
}

func proposalQuestions() map[string]tn.Question {
	return map[string]tn.Question{proposalQuestion: tn.NoulQuestion{
		Instructions: "Is this workflow change correct, coherent and safe to merge as it is?",
		Criteria: &tn.NoulCriteria{
			True:  "The change is valid, does what it appears to intend, and introduces no secrets, destructive steps or broken references.",
			False: "The change is invalid, incoherent, leaks a credential, removes a safety gate, or would break the workflow.",
		},
	}}
}

// ReviewProposal runs OUR classifier (the same judge a `decide:` step uses)
// over a proposal's diff and its validation result. It advises; it never
// fails a proposal: no classifier, or a classifier that errs, is
// "unreviewed" with the reason.
func (e *Engine) ReviewProposal(ctx context.Context, diff, validation string) Review {
	if !e.classifierConfigured() {
		return ReviewUnreviewed("no classifier configured")
	}
	c, err := e.classifierFor(nil)
	if err != nil {
		return ReviewUnreviewed("classifier unavailable: " + scrub(err.Error()))
	}
	const maxDiff = 24000
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n… (diff truncated)"
	}
	state, qs := proposalState(diff, validation), proposalQuestions()
	d, err := c.Evaluate(ctx, state, qs)
	if err != nil {
		return ReviewUnreviewed("classifier error: " + scrub(err.Error()))
	}
	a, err := d.Noul(proposalQuestion)
	if err != nil {
		return ReviewUnreviewed("classifier answer unreadable: " + scrub(err.Error()))
	}
	p := a.Noul
	r := Review{Score: &p}
	if p >= 0.5 {
		r.Verdict, r.Reason = "approve", fmt.Sprintf("classifier: safe to merge (p=%.2f)", p)
	} else {
		r.Verdict, r.Reason = "reject", fmt.Sprintf("classifier: not safe to merge (p=%.2f)", p)
	}
	if strings.TrimSpace(validation) != "" && !strings.HasPrefix(validation, "ok") {
		r.Reason += "; validation: " + validation
	}
	return r
}
