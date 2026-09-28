package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// skillRepo is a bare repository fixture plus a working clone that commits to it.
type skillRepo struct {
	t    *testing.T
	bare string
	work string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newSkillRepo(t *testing.T) *skillRepo {
	t.Helper()
	root := t.TempDir()
	r := &skillRepo{t: t, bare: filepath.Join(root, "skills.git"), work: filepath.Join(root, "work")}
	runGit(t, root, "init", "-q", "--bare", "-b", "main", r.bare)
	runGit(t, root, "init", "-q", "-b", "main", r.work)
	runGit(t, r.work, "remote", "add", "origin", r.bare)
	return r
}

func (r *skillRepo) skill(path, name, desc string) {
	r.t.Helper()
	dir := filepath.Join(r.work, filepath.FromSlash(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *skillRepo) commit(msg string) string {
	r.t.Helper()
	runGit(r.t, r.work, "add", "-A")
	runGit(r.t, r.work, "commit", "-q", "-m", msg)
	runGit(r.t, r.work, "push", "-q", "origin", "HEAD")
	return runGit(r.t, r.work, "rev-parse", "HEAD")
}

func skillSourceEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{WorkDir: filepath.Join(dir, "work"), SkillsDir: filepath.Join(dir, "own-skills"),
		WorkflowsDir: filepath.Join(dir, "workflows")}
	for _, d := range []string{cfg.WorkDir, cfg.SkillsDir, cfg.WorkflowsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", dir) // keep the machine's ~/.claude/skills out of the registry
	return New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(cfg.SkillsDir), nil)
}

func names(e *Engine, source string) []string {
	var out []string
	for _, s := range e.Skills().List() {
		if s.Source == source {
			out = append(out, s.Name)
		}
	}
	return out
}

func TestImportSkillSourceLoadsEverySkillUnderThePath(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.skill("skills/group/beta", "beta", "nested")
	repo.skill("docs/not-a-skill-dir", "outside", "not under the path")
	repo.commit("init")
	e := skillSourceEngine(t)

	rep, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{URL: repo.bare, Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(e, "skills"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("registry from source = %v", got)
	}
	if !reflect.DeepEqual(rep.Added, []string{"alpha", "beta"}) || rep.Source.RefKind != "branch" ||
		rep.Source.Branch != "main" || len(rep.Source.Commit) != 40 || rep.Source.Count != 2 {
		t.Fatalf("report = %+v", rep)
	}
	list, _ := e.SkillSources()
	if len(list) != 1 || list[0].Path != "skills" || list[0].URL != repo.bare {
		t.Fatalf("persisted = %+v", list)
	}
	// "." takes the whole repository.
	rep, err = e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "whole", URL: repo.bare, Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source.Count != 3 || rep.Source.Ref != "main" {
		t.Fatalf("whole-repo import = %+v", rep.Source)
	}
}

func TestSwitchingASkillSourceBranchChangesTheSetAndReportsTheDiff(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.skill("skills/beta", "beta", "second")
	repo.commit("main")
	runGit(t, repo.work, "checkout", "-q", "-b", "next")
	if err := os.RemoveAll(filepath.Join(repo.work, "skills", "beta")); err != nil {
		t.Fatal(err)
	}
	repo.skill("skills/gamma", "gamma", "only on next")
	repo.skill("skills/alpha", "alpha", "first, rewritten")
	runGit(t, repo.work, "add", "-A")
	runGit(t, repo.work, "commit", "-q", "-m", "next")
	runGit(t, repo.work, "push", "-q", "origin", "next")
	e := skillSourceEngine(t)
	ctx := context.Background()

	if _, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "s", URL: repo.bare, Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	rep, err := e.SetSkillSourceRef(ctx, "s", "next")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Added, []string{"gamma"}) || !reflect.DeepEqual(rep.Removed, []string{"beta"}) ||
		!reflect.DeepEqual(rep.Updated, []string{"alpha"}) {
		t.Fatalf("diff = +%v -%v ~%v", rep.Added, rep.Removed, rep.Updated)
	}
	if got := names(e, "s"); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("registry after switch = %v", got)
	}
	if rep.Source.Branch != "next" {
		t.Fatalf("branch not recorded: %+v", rep.Source)
	}
	// A ref that does not exist changes nothing.
	if _, err := e.SetSkillSourceRef(ctx, "s", "nope"); err == nil {
		t.Fatal("a missing branch was accepted")
	}
	if got := names(e, "s"); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("a failed switch changed the set: %v", got)
	}
}

func TestRefreshingABranchSourcePicksUpANewCommit(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.commit("one")
	e := skillSourceEngine(t)
	ctx := context.Background()
	if _, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "s", URL: repo.bare}); err != nil {
		t.Fatal(err)
	}
	repo.skill("skills/delta", "delta", "new")
	head := repo.commit("two")

	rep, err := e.RefreshSkillSource(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Added, []string{"delta"}) || rep.Source.Commit != head || rep.Pinned {
		t.Fatalf("refresh = %+v", rep)
	}
	if !e.Skills().Has("delta") {
		t.Fatal("the new skill is not in the registry")
	}
}

func TestATagOrCommitSourceIsPinnedAndRefreshSaysSo(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	first := repo.commit("one")
	runGit(t, repo.work, "tag", "v1.0.0")
	runGit(t, repo.work, "push", "-q", "origin", "v1.0.0")
	repo.skill("skills/beta", "beta", "later")
	repo.commit("two")
	e := skillSourceEngine(t)
	ctx := context.Background()

	rep, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "tagged", URL: repo.bare, Ref: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source.RefKind != "tag" || rep.Source.Commit != first || rep.Source.Branch != "" {
		t.Fatalf("tag import = %+v", rep.Source)
	}
	if got := names(e, "tagged"); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("tag set = %v", got)
	}
	rep, err = e.RefreshSkillSource(ctx, "tagged")
	if err != nil || !rep.Pinned || !strings.Contains(rep.Message, "pinned") {
		t.Fatalf("refresh on a tag = %+v, %v", rep, err)
	}

	// Switch the same source to a commit id, full and short.
	rep, err = e.SetSkillSourceRef(ctx, "tagged", "main")
	if err != nil || !reflect.DeepEqual(rep.Added, []string{"beta"}) {
		t.Fatalf("to main = %+v, %v", rep, err)
	}
	rep, err = e.SetSkillSourceRef(ctx, "tagged", first)
	if err != nil || rep.Source.RefKind != "commit" || !reflect.DeepEqual(rep.Removed, []string{"beta"}) {
		t.Fatalf("to a full sha = %+v, %v", rep, err)
	}
	rep, err = e.RefreshSkillSource(ctx, "tagged")
	if err != nil || !rep.Pinned {
		t.Fatalf("refresh on a commit = %+v, %v", rep, err)
	}

	// Import directly at a short commit id.
	rep, err = e.ImportSkillSource(ctx, SkillSourceSpec{Name: "bysha", URL: repo.bare, Ref: first[:10]})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source.RefKind != "commit" || rep.Source.Commit != first || rep.Source.Count != 1 {
		t.Fatalf("sha import = %+v", rep.Source)
	}
}

func TestASkillNameClashIsReportedAndTheFirstClaimerKeepsIt(t *testing.T) {
	e := skillSourceEngine(t)
	own := filepath.Join(e.cfg.SkillsDir, "alpha")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("---\nname: alpha\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "theirs")
	repo.skill("skills/beta", "beta", "fine")
	repo.commit("init")

	rep, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "s", URL: repo.bare})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Source.Clashes) != 1 || rep.Source.Clashes[0].Skill != "alpha" || rep.Source.Clashes[0].OwnedBy != "project" {
		t.Fatalf("clashes = %+v", rep.Source.Clashes)
	}
	if s, _ := e.Skills().Get("alpha"); s.Description != "mine" || len(s.Shadowed) != 1 {
		t.Fatalf("the existing skill was overwritten: %+v", s)
	}
}

func TestASkillSourcePathOrRefOrURLThatEscapesIsRefused(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.commit("init")
	e := skillSourceEngine(t)
	ctx := context.Background()

	for _, p := range []string{"../", "..", "skills/../../x", "/etc", "../../work"} {
		if _, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "p", URL: repo.bare, Path: p}); err == nil ||
			!strings.Contains(err.Error(), "leaves the repository") {
			t.Fatalf("path %q: err = %v", p, err)
		}
	}
	for _, r := range []string{"--upload-pack=touch /tmp/x", "main;rm -rf /", "a..b", "$(id)", "-b"} {
		if _, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "r", URL: repo.bare, Ref: r}); err == nil {
			t.Fatalf("ref %q was accepted", r)
		}
	}
	for _, u := range []string{"ext::sh -c touch% /tmp/pwned", "--upload-pack=x", "relative/path", "fd::3"} {
		if _, err := e.ImportSkillSource(ctx, SkillSourceSpec{Name: "u", URL: u}); err == nil {
			t.Fatalf("url %q was accepted", u)
		}
	}
	if list, _ := e.SkillSources(); len(list) != 0 {
		t.Fatalf("a refused import was recorded: %+v", list)
	}
	entries, _ := os.ReadDir(filepath.Join(e.cfg.WorkDir, "skill-sources"))
	if len(entries) != 0 {
		t.Fatalf("a refused import left a checkout: %v", entries)
	}
}

func TestASymlinkedSkillFileIsCheckedOutAsPlainData(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	secret := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(secret, []byte("---\nname: leaked\ndescription: outside\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.work, "skills", "evil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(repo.work, "skills", "evil", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	repo.commit("init")
	e := skillSourceEngine(t)
	if _, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "s", URL: repo.bare}); err != nil {
		t.Fatal(err)
	}
	if e.Skills().Has("leaked") {
		t.Fatal("a symlink in an imported repo read a file outside the checkout")
	}
}

func TestRemovingASkillSourceUnloadsItsSkillsAndDeletesTheCheckout(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.commit("init")
	e := skillSourceEngine(t)
	if _, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "s", URL: repo.bare}); err != nil {
		t.Fatal(err)
	}
	rep, err := e.RemoveSkillSource("s")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Removed, []string{"alpha"}) || e.Skills().Has("alpha") {
		t.Fatalf("remove = %+v, still loaded = %v", rep, e.Skills().Has("alpha"))
	}
	if _, err := os.Stat(e.skillSourceDir("s")); !os.IsNotExist(err) {
		t.Fatalf("checkout still on disk: %v", err)
	}
	if _, err := e.RemoveSkillSource("s"); err == nil {
		t.Fatal("removing twice succeeded")
	}
}

func TestSkillSourceRefsListsBranchesAndTags(t *testing.T) {
	repo := newSkillRepo(t)
	repo.skill("skills/alpha", "alpha", "first")
	repo.commit("init")
	runGit(t, repo.work, "tag", "v1")
	runGit(t, repo.work, "push", "-q", "origin", "v1", "HEAD:refs/heads/dev")
	e := skillSourceEngine(t)
	if _, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "s", URL: repo.bare}); err != nil {
		t.Fatal(err)
	}
	refs, err := e.SkillSourceRefs(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs.Branches, []string{"dev", "main"}) || !reflect.DeepEqual(refs.Tags, []string{"v1"}) {
		t.Fatalf("refs = %+v", refs)
	}
	files, err := e.SkillSourceSkills("s")
	if err != nil || len(files) != 1 || !strings.Contains(files[0].Content, "# alpha") || !files[0].Loaded {
		t.Fatalf("skills = %+v, %v", files, err)
	}
}
