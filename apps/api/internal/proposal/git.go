// Package proposal makes a workflow change a git change.
//
// Workflows are git-native: they live in a project's `.wfx/workflows/`, and a
// save or a delete from the builder on a project that is a git repository does
// not write into the tracked checkout. It opens a PROPOSAL instead — a branch
// `wfx/edit-<workflow>-<unix ts>` cut in its own worktree, the change written
// and committed there by "wfx", pushed and opened as a pull request when the
// repository has a remote. A person approves (the branch merges, the checkout
// pulls, the catalog reloads) or rejects (the PR closes, the branch goes).
//
// A repository with no remote is SOLO mode: the proposal branch still exists,
// so the change is reviewable, and approve merges it locally.
//
// This package is only the git half. Persisting proposals is the store's job,
// deciding who may approve is the API's.
package proposal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Author is who a proposal commit says wrote it. The person who asked is
// recorded on the proposal row (created_by); the commit is the platform's.
const (
	AuthorName  = "wfx"
	AuthorEmail = "wfx@localhost"
)

// Kinds of change.
const (
	KindCreate = "create"
	KindEdit   = "edit"
	KindDelete = "delete"
)

// Statuses. pending → approved → merged is the happy path; approved is only
// visible when the merge itself failed after a person said yes.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusMerged   = "merged"
)

// Run executes a command in dir and returns its combined output. A variable so
// a test can see which commands ran; never replaced in production.
var Run = func(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// A proposal is never interactive: no credential prompt, no editor.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GH_PROMPT_DISABLED=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return Run(ctx, dir, "git", args...)
}

// Repo is where a project's workflows sit inside a git repository.
type Repo struct {
	Root string // the repository's top level
	Rel  string // the workflows directory, relative to Root, slash-separated
}

// Detect answers whether a workflows directory is inside a git work tree.
// ok=false is not an error: a plain directory keeps direct writes.
func Detect(ctx context.Context, workflowsDir string) (Repo, bool) {
	if workflowsDir == "" {
		return Repo{}, false
	}
	// The directory may not exist yet (a project's first workflow). Ask from
	// the nearest parent that does.
	probe := workflowsDir
	for {
		if st, err := os.Stat(probe); err == nil && st.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return Repo{}, false
		}
		probe = parent
	}
	top, err := git(ctx, probe, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return Repo{}, false
	}
	// Symlinked temp dirs (macOS /var → /private/var) make Rel lie unless
	// both sides are resolved.
	rootReal, err1 := filepath.EvalSymlinks(top)
	absDir, _ := filepath.Abs(workflowsDir)
	dirReal := resolveExisting(absDir)
	if err1 != nil {
		rootReal = top
	}
	rel, err := filepath.Rel(rootReal, dirReal)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Repo{}, false
	}
	// A repository with no commit yet has no HEAD to branch from.
	if _, err := git(ctx, rootReal, "rev-parse", "--verify", "HEAD"); err != nil {
		return Repo{}, false
	}
	return Repo{Root: rootReal, Rel: filepath.ToSlash(rel)}, true
}

// resolveExisting resolves symlinks on the longest existing prefix of p.
func resolveExisting(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(p))
}

// Remote returns the remote a proposal is pushed to — origin when there is
// one, otherwise the first — or "" in solo mode.
func (r Repo) Remote(ctx context.Context) string {
	out, err := git(ctx, r.Root, "remote")
	if err != nil || out == "" {
		return ""
	}
	names := strings.Fields(out)
	for _, n := range names {
		if n == "origin" {
			return n
		}
	}
	return names[0]
}

// CurrentBranch is the branch the tracked checkout is on — the proposal's base.
func (r Repo) CurrentBranch(ctx context.Context) (string, error) {
	b, err := git(ctx, r.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if b == "HEAD" {
		return "", errors.New("the checkout is on a detached HEAD; a proposal needs a branch to merge into")
	}
	return b, nil
}

// BranchName is `wfx/edit-<workflow>-<unix ts>`.
func BranchName(workflow string, at time.Time) string {
	return fmt.Sprintf("wfx/edit-%s-%d", workflow, at.Unix())
}

// Change describes one proposal to open.
type Change struct {
	Workflow string
	Kind     string
	Message  string // the commit message; also the PR title's first line
	Body     string // PR body
	WorkDir  string // where the temporary worktree goes
	// Apply writes the change into the worktree's workflows directory.
	Apply func(workflowsDir string) error
}

// Opened is what a successful Open reports back.
type Opened struct {
	Branch string
	Base   string
	Commit string
	PRURL  string
	Remote string
	// Note records a non-fatal failure (the push or the PR), so the proposal
	// still exists locally and says why it has no PR.
	Note string
}

// ErrNoChange is returned when applying the change left the tree identical.
var ErrNoChange = errors.New("nothing to propose: the change leaves the workflow as it is")

// Open cuts the branch in a fresh worktree, applies and commits the change,
// removes the worktree (the branch keeps the commit), and pushes + opens a PR
// when there is a remote.
func (r Repo) Open(ctx context.Context, c Change) (*Opened, error) {
	base, err := r.CurrentBranch(ctx)
	if err != nil {
		return nil, err
	}
	branch := BranchName(c.Workflow, time.Now())
	// Two saves of one workflow in the same second must not collide.
	for i := 2; r.branchExists(ctx, branch); i++ {
		branch = fmt.Sprintf("%s-%d", BranchName(c.Workflow, time.Now()), i)
	}
	if err := os.MkdirAll(c.WorkDir, 0o755); err != nil {
		return nil, err
	}
	wt, err := os.MkdirTemp(c.WorkDir, "proposal-")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(wt) // git worktree add wants to create it
	if _, err := git(ctx, r.Root, "worktree", "add", "-b", branch, wt, base); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = git(context.WithoutCancel(ctx), r.Root, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}()
	fail := func(err error) (*Opened, error) {
		_, _ = git(context.WithoutCancel(ctx), r.Root, "worktree", "remove", "--force", wt)
		_, _ = git(context.WithoutCancel(ctx), r.Root, "branch", "-D", branch)
		return nil, err
	}
	wfDir := filepath.Join(wt, filepath.FromSlash(r.Rel))
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return fail(err)
	}
	if err := c.Apply(wfDir); err != nil {
		return fail(err)
	}
	if _, err := git(ctx, wt, "add", "-A", "--", filepath.FromSlash(r.Rel)); err != nil {
		return fail(err)
	}
	if _, err := git(ctx, wt, "diff", "--cached", "--quiet"); err == nil {
		return fail(ErrNoChange)
	}
	if _, err := git(ctx, wt, commitArgs(c.Message)...); err != nil {
		return fail(err)
	}
	commit, _ := git(ctx, wt, "rev-parse", "HEAD")
	o := &Opened{Branch: branch, Base: base, Commit: commit}

	if remote := r.Remote(ctx); remote != "" {
		o.Remote = remote
		if _, err := git(ctx, r.Root, "push", "-u", remote, branch); err != nil {
			o.Note = "push failed; the proposal is local only: " + err.Error()
			return o, nil
		}
		title := firstLine(c.Message)
		out, err := Run(ctx, r.Root, "gh", "pr", "create", "--head", branch, "--base", base,
			"--title", title, "--body", c.Body)
		if err != nil {
			o.Note = "pushed, but no pull request was opened: " + err.Error()
			return o, nil
		}
		o.PRURL = lastURL(out)
	}
	return o, nil
}

func commitArgs(msg string) []string {
	return []string{"-c", "user.name=" + AuthorName, "-c", "user.email=" + AuthorEmail,
		"-c", "commit.gpgsign=false",
		"commit", "--author", AuthorName + " <" + AuthorEmail + ">", "-m", msg}
}

func (r Repo) branchExists(ctx context.Context, b string) bool {
	_, err := git(ctx, r.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
	return err == nil
}

// Diff is the change a proposal makes, against the base it was cut from.
func (r Repo) Diff(ctx context.Context, base, branch string) (string, error) {
	if !r.branchExists(ctx, branch) {
		// Merged and deleted, or rejected: nothing to show, and not an error.
		return "", nil
	}
	return git(ctx, r.Root, "diff", base+"..."+branch)
}

// Merge lands an approved proposal. With a PR it is `gh pr merge` and then
// a pull of the tracked checkout; without one, a local merge into the
// checkout's branch (solo mode).
func (r Repo) Merge(ctx context.Context, base, branch, prURL string) error {
	if prURL != "" {
		if _, err := Run(ctx, r.Root, "gh", "pr", "merge", prURL, "--merge", "--delete-branch"); err != nil {
			return err
		}
		remote := r.Remote(ctx)
		if _, err := git(ctx, r.Root, "fetch", remote); err != nil {
			return err
		}
		if err := r.cleanMatching(ctx, remote+"/"+base); err != nil {
			return err
		}
		if _, err := git(ctx, r.Root, "pull", "--ff-only"); err != nil {
			return err
		}
		_, _ = git(ctx, r.Root, "branch", "-D", branch)
		return nil
	}
	cur, err := r.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	if cur != base {
		return fmt.Errorf("the checkout is on %q, the proposal targets %q; switch back to merge it", cur, base)
	}
	if err := r.cleanMatching(ctx, branch); err != nil {
		return err
	}
	if _, err := git(ctx, r.Root, append([]string{"-c", "user.name=" + AuthorName, "-c", "user.email=" + AuthorEmail,
		"-c", "commit.gpgsign=false"}, "merge", "--no-edit", branch)...); err != nil {
		_, _ = git(context.WithoutCancel(ctx), r.Root, "merge", "--abort")
		return err
	}
	_, _ = git(ctx, r.Root, "branch", "-d", branch)
	return nil
}

// cleanMatching clears working-copy changes under the workflows directory that
// are EXACTLY what the proposal branch already carries — the drift a proposal
// was made from. Without it a merge refuses ("would be overwritten"). A local
// change that differs from the branch is left alone, and the merge then says
// so, rather than losing somebody's edit.
func (r Repo) cleanMatching(ctx context.Context, ref string) error {
	entries, err := r.status(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		want, err := git(ctx, r.Root, "show", ref+":"+e.Path)
		if err != nil {
			continue // the branch does not have this file: not ours to touch
		}
		have, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(e.Path)))
		if err != nil || strings.TrimSpace(string(have)) != want {
			continue
		}
		if e.Untracked {
			_ = os.Remove(filepath.Join(r.Root, filepath.FromSlash(e.Path)))
		} else {
			_, _ = git(ctx, r.Root, "checkout", "HEAD", "--", e.Path)
		}
	}
	return nil
}

// Close rejects a proposal: the PR is closed and the branch goes.
func (r Repo) Close(ctx context.Context, branch, prURL, reason string) error {
	if prURL != "" {
		args := []string{"pr", "close", prURL, "--delete-branch"}
		if reason != "" {
			args = append(args, "--comment", "Rejected: "+reason)
		}
		if _, err := Run(ctx, r.Root, "gh", args...); err != nil {
			return err
		}
	}
	if r.branchExists(ctx, branch) {
		if _, err := git(ctx, r.Root, "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

type statusEntry struct {
	Path      string
	Untracked bool
	Deleted   bool
}

func (r Repo) status(ctx context.Context) ([]statusEntry, error) {
	// porcelain v2: every line starts with its record type, so the output
	// survives Run's whitespace trim (v1's " M path" would not).
	out, err := git(ctx, r.Root, "status", "--porcelain=v2", "--untracked-files=all", "--", r.Rel)
	if err != nil {
		return nil, err
	}
	var es []statusEntry
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "?":
			es = append(es, statusEntry{Path: strings.Trim(strings.TrimPrefix(line, "? "), `"`), Untracked: true})
		case "1":
			if len(f) < 9 {
				continue
			}
			es = append(es, statusEntry{Path: strings.Trim(strings.Join(f[8:], " "), `"`), Deleted: strings.Contains(f[1], "D")})
		case "2":
			if len(f) < 10 {
				continue
			}
			// "<path>\t<origPath>": the new path is the one that is on disk.
			es = append(es, statusEntry{Path: strings.Trim(f[9], `"`)})
		}
	}
	return es, nil
}

// Drift is one workflow whose files in the checkout are not what is committed.
type Drift struct {
	Workflow string   `json:"workflow"`
	Kind     string   `json:"kind"` // create, edit or delete
	Files    []string `json:"files"`
}

// Drift lists workflows with uncommitted or untracked changes under the
// workflows directory — a folder somebody edited by hand, or a new one
// nobody committed.
func (r Repo) Drift(ctx context.Context) ([]Drift, error) {
	es, err := r.status(ctx)
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(r.Rel, "/") + "/"
	if r.Rel == "." {
		prefix = ""
	}
	type acc struct {
		files                     []string
		untracked, deleted, other bool
	}
	by := map[string]*acc{}
	for _, e := range es {
		rest := strings.TrimPrefix(e.Path, prefix)
		name := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name = rest[:i]
		} else {
			name = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		}
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		a := by[name]
		if a == nil {
			a = &acc{}
			by[name] = a
		}
		a.files = append(a.files, e.Path)
		switch {
		case e.Untracked:
			a.untracked = true
		case e.Deleted:
			a.deleted = true
		default:
			a.other = true
		}
	}
	out := []Drift{}
	for name, a := range by {
		kind := KindEdit
		switch {
		case a.untracked && !a.deleted && !a.other && !r.tracked(ctx, prefix+name):
			kind = KindCreate
		case a.deleted && !a.untracked && !a.other && !exists(filepath.Join(r.Root, filepath.FromSlash(prefix+name))) &&
			!exists(filepath.Join(r.Root, filepath.FromSlash(prefix+name+".yaml"))):
			kind = KindDelete
		}
		sort.Strings(a.files)
		out = append(out, Drift{Workflow: name, Kind: kind, Files: a.files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workflow < out[j].Workflow })
	return out, nil
}

func (r Repo) tracked(ctx context.Context, p string) bool {
	out, _ := git(ctx, r.Root, "ls-files", "--", p, p+".yaml", p+".yml")
	return strings.TrimSpace(out) != ""
}

// CopyDrift is a Change.Apply that mirrors the checkout's copy of the given
// files into the proposal worktree — present files are copied, deleted ones
// removed — so the proposal carries exactly what is on disk.
func (r Repo) CopyDrift(d Drift) func(string) error {
	return func(wfDir string) error {
		wtRoot := wfDir
		for range strings.Split(strings.Trim(r.Rel, "/"), "/") {
			if r.Rel == "." {
				break
			}
			wtRoot = filepath.Dir(wtRoot)
		}
		for _, f := range d.Files {
			src := filepath.Join(r.Root, filepath.FromSlash(f))
			dst := filepath.Join(wtRoot, filepath.FromSlash(f))
			raw, err := os.ReadFile(src)
			if os.IsNotExist(err) {
				_ = os.Remove(dst)
				continue
			}
			if err != nil {
				return err
			}
			st, _ := os.Stat(src)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, raw, st.Mode().Perm()); err != nil {
				return err
			}
		}
		return nil
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// lastURL picks the PR url out of gh's output, which may carry warnings first.
func lastURL(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "http") {
			return l
		}
	}
	return ""
}
