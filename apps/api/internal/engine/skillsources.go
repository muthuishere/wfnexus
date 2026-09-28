package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
)

// A skill source is a git repository of skills — every directory under `path`
// holding a SKILL.md — imported into the registry at a ref the operator picks
// and can change later: a branch (which a sync follows), or a tag or commit
// (which a sync leaves alone, because a pin that moves is not a pin).
//
// It is the skills-side twin of a project source (sources.go): the list is
// machine state, so it lives in the runtime dir beside sources.json, and the
// checkout lives under the work dir beside the project clones. It shares that
// file's git runner (gitIn), so there is one place that decides how this
// platform talks to a repository it did not write.
//
// Imported content is DATA. Nothing from the repository is executed at import:
// no hooks (a fresh `git init` has none and core.hooksPath is pinned to an
// empty dir), no submodules, no filters we did not configure, and symlinks
// are checked out as plain files (core.symlinks=false), so a SKILL.md cannot
// be a link to something outside the checkout.

const skillSourcesFile = "skill-sources.json"

// Bounds. A skills repo is text; a checkout far past this is not one, and
// importing it would be a way to fill the server's disk.
const (
	maxSkillSourceBytes  = 100 << 20
	maxSkillsPerSource   = 500
	maxSkillContentBytes = 256 << 10
)

var skillSourceMu sync.Mutex

// SkillSource is one imported repository of skills.
type SkillSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Ref is what the operator asked for; RefKind is what it turned out to be:
	// "branch", "tag" or "commit". Commit is what it resolved to at last sync.
	Ref     string `json:"ref"`
	RefKind string `json:"refKind"`
	// Branch repeats Ref when RefKind is "branch", for readers that only
	// know branches.
	Branch   string    `json:"branch,omitempty"`
	Path     string    `json:"path"`
	Commit   string    `json:"commit"`
	SyncedAt time.Time `json:"syncedAt"`
}

// SkillSourceView is a source plus what it currently contributes.
type SkillSourceView struct {
	SkillSource
	Skills []string `json:"skills"`
	Count  int      `json:"count"`
	// Clashes are this source's skills whose name an earlier root already
	// owns. They are not loaded under that name; the owner keeps it.
	Clashes []SkillClash `json:"clashes,omitempty"`
}

// SkillClash is one name a source wanted and did not get.
type SkillClash struct {
	Skill    string `json:"skill"`
	OwnedBy  string `json:"ownedBy"`
	Location string `json:"location"`
}

// SkillSourceReport is what an import, ref switch, sync or remove changed.
type SkillSourceReport struct {
	Source  SkillSourceView `json:"source"`
	Added   []string        `json:"added"`
	Removed []string        `json:"removed"`
	Updated []string        `json:"updated"`
	// Pinned is set when a sync had nothing to do because the ref is a tag or
	// a commit; Message says so in words.
	Pinned  bool   `json:"pinned,omitempty"`
	Message string `json:"message"`
}

// SkillSourceSpec is an import request.
type SkillSourceSpec struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

// SkillFile is one skill of a source, with its SKILL.md for read-only display.
type SkillFile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Loaded      bool   `json:"loaded"`
}

// RemoteRefs are the branches and tags a remote advertises.
type RemoteRefs struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
	Current  string   `json:"current"`
}

// ---- validation --------------------------------------------------------------

// safeRef is what a branch, tag or commit may look like: no leading dash (it
// would read as a flag), no whitespace or shell/refspec metacharacters, no
// "..". It is stricter than git's own rule on purpose — it enumerates what is
// allowed.
var safeRef = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/+-]{0,199}$`)
var hexSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func validateRef(ref string) error {
	if !safeRef.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, ".lock") ||
		strings.HasSuffix(ref, "/") || strings.Contains(ref, "//") {
		return fmt.Errorf("ref %q is not a branch, tag or commit name this platform accepts "+
			"(letters, digits, and . _ / + -; no leading dash, no \"..\")", ref)
	}
	return nil
}

// validateSkillURL allowlists how a remote may be named. The same rule as a
// remote `use:` (workflow/remoteref.go): a scheme we name, scp-style, or a local
// absolute path to a repository — never ext:: or any other transport helper,
// which can run a command, and never something git would read as a flag.
func validateSkillURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("a git URL is required")
	}
	if strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n\x00") {
		return fmt.Errorf("%q is not a git URL", raw)
	}
	if filepath.IsAbs(raw) {
		return nil // a repository on this machine, e.g. a bare mirror
	}
	if i := strings.Index(raw, "://"); i > 0 {
		switch strings.ToLower(raw[:i]) {
		case "https", "http", "ssh", "git", "file":
			return nil
		}
		return fmt.Errorf("%q: only https, http, ssh, git and file URLs are accepted", raw)
	}
	if i := strings.Index(raw, ":"); i > 0 && strings.Contains(raw[:i], "@") && !strings.Contains(raw[:i], "/") {
		return nil // git@host:org/repo
	}
	return fmt.Errorf("%q is not a git URL — use https://github.com/owner/repo, git@host:owner/repo or an absolute path", raw)
}

// cleanSkillPath is the directory inside the repository to load from. It may
// be "." (the whole repository) and may never leave it.
func cleanSkillPath(p string) (string, error) {
	if p == "" {
		p = "skills"
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") ||
		strings.Contains(p, ":") || strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("path %q leaves the repository — it must be a directory inside it, like \"skills\" or \".\"", p)
	}
	return p, nil
}

// reservedSkillLabels are the labels the registry already gives built-in roots.
var reservedSkillLabels = map[string]bool{"project": true, "claude": true, "agents": true}

// ---- persistence -------------------------------------------------------------

func (e *Engine) skillSourcesPath() string { return filepath.Join(e.cfg.WorkDir, skillSourcesFile) }

func (e *Engine) skillSourceDir(name string) string {
	return filepath.Join(e.cfg.WorkDir, "skill-sources", name)
}

func (e *Engine) readSkillSources() ([]SkillSource, error) {
	raw, err := os.ReadFile(e.skillSourcesPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SkillSource
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", e.skillSourcesPath(), err)
	}
	return out, nil
}

func (e *Engine) writeSkillSources(list []SkillSource) error {
	if err := os.MkdirAll(e.cfg.WorkDir, 0o755); err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.skillSourcesPath() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.skillSourcesPath())
}

// skillSourceRoots are the registry roots the imported sources contribute, in
// name order, labelled with the source name.
func (e *Engine) skillSourceRoots() []skills.Root {
	list, err := e.readSkillSources()
	if err != nil {
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]skills.Root, 0, len(list))
	for _, s := range list {
		out = append(out, skills.Root{Dir: e.skillSourceRoot(s), Label: s.Name})
	}
	return out
}

func (e *Engine) skillSourceRoot(s SkillSource) string {
	return filepath.Join(e.skillSourceDir(s.Name), filepath.FromSlash(s.Path))
}

func findSkillSource(list []SkillSource, name string) (int, error) {
	for i := range list {
		if list[i].Name == name {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no skill source named %q (`wfx skills sources` lists them)", name)
}

// ---- operations --------------------------------------------------------------

// SkillSources lists every imported source with what it contributes now.
func (e *Engine) SkillSources() ([]SkillSourceView, error) {
	list, err := e.readSkillSources()
	if err != nil {
		return nil, err
	}
	out := make([]SkillSourceView, 0, len(list))
	for _, s := range list {
		out = append(out, e.viewOf(s))
	}
	return out, nil
}

// ImportSkillSource clones a repository at a ref and loads every skill under
// its path. A name already imported is refused: changing what an existing
// source points at is SetSkillSourceRef, which reports the difference.
func (e *Engine) ImportSkillSource(ctx context.Context, spec SkillSourceSpec) (SkillSourceReport, error) {
	skillSourceMu.Lock()
	defer skillSourceMu.Unlock()

	if err := validateSkillURL(spec.URL); err != nil {
		return SkillSourceReport{}, err
	}
	name := spec.Name
	if name == "" {
		name = inferName(spec.URL)
	}
	if !safeName.MatchString(name) || reservedSkillLabels[name] {
		return SkillSourceReport{}, fmt.Errorf("skill source name %q must start alphanumeric and contain only "+
			"letters, digits, dot, dash and underscore, and cannot be project, claude or agents", name)
	}
	if spec.Ref != "" {
		if err := validateRef(spec.Ref); err != nil {
			return SkillSourceReport{}, err
		}
	}
	path, err := cleanSkillPath(spec.Path)
	if err != nil {
		return SkillSourceReport{}, err
	}
	list, err := e.readSkillSources()
	if err != nil {
		return SkillSourceReport{}, err
	}
	if _, err := findSkillSource(list, name); err == nil {
		return SkillSourceReport{}, fmt.Errorf("a skill source named %q already exists — "+
			"`wfx skills ref %s <ref>` switches it, `wfx skills remove %s` removes it", name, name, name)
	}

	// Checked out beside its final place and renamed in only once it passed,
	// so a refused import leaves nothing behind.
	final := e.skillSourceDir(name)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return SkillSourceReport{}, err
	}
	tmp := filepath.Join(filepath.Dir(final), ".import-"+name+"-"+randHex())
	defer os.RemoveAll(tmp)

	env := e.gitCredentialEnv(ctx, spec.URL)
	if err := initCheckout(ctx, tmp, spec.URL, path); err != nil {
		return SkillSourceReport{}, err
	}
	ref := spec.Ref
	if ref == "" {
		if ref, err = defaultBranch(ctx, tmp, env); err != nil {
			return SkillSourceReport{}, err
		}
	}
	kind, commit, err := checkoutRef(ctx, tmp, ref, env)
	if err != nil {
		return SkillSourceReport{}, err
	}
	src := SkillSource{Name: name, URL: spec.URL, Ref: ref, RefKind: kind, Path: path, Commit: commit,
		SyncedAt: time.Now().UTC()}
	if kind == "branch" {
		src.Branch = ref
	}
	if _, err := checkSkillCheckout(tmp, path); err != nil {
		return SkillSourceReport{}, err
	}
	_ = os.RemoveAll(final) // a leftover from a crashed import, never a live source (checked above)
	if err := os.Rename(tmp, final); err != nil {
		return SkillSourceReport{}, err
	}
	after := snapshotSkills(e.skillSourceRoot(src), name)
	if err := e.writeSkillSources(append(list, src)); err != nil {
		_ = os.RemoveAll(final)
		return SkillSourceReport{}, err
	}
	if err := e.ReloadDefinitions(); err != nil {
		return SkillSourceReport{}, err
	}
	rep := diffReport(nil, after)
	rep.Source = e.viewOf(src)
	rep.Message = fmt.Sprintf("imported %d skill(s) from %s at %s %s (%s)", rep.Source.Count, src.URL, kind, ref, short(commit))
	return rep, nil
}

// SetSkillSourceRef switches a source to another branch, tag or commit and
// reports what that added, removed or changed. A switch that fails leaves the
// source where it was.
func (e *Engine) SetSkillSourceRef(ctx context.Context, name, ref string) (SkillSourceReport, error) {
	if err := validateRef(ref); err != nil {
		return SkillSourceReport{}, err
	}
	return e.resyncSkillSource(ctx, name, ref)
}

// RefreshSkillSource pulls a branch source again. A tag or commit is fixed, so
// it is a no-op that says so rather than a fetch that could only find the same
// thing.
func (e *Engine) RefreshSkillSource(ctx context.Context, name string) (SkillSourceReport, error) {
	return e.resyncSkillSource(ctx, name, "")
}

func (e *Engine) resyncSkillSource(ctx context.Context, name, ref string) (SkillSourceReport, error) {
	skillSourceMu.Lock()
	defer skillSourceMu.Unlock()

	list, err := e.readSkillSources()
	if err != nil {
		return SkillSourceReport{}, err
	}
	i, err := findSkillSource(list, name)
	if err != nil {
		return SkillSourceReport{}, err
	}
	src := list[i]
	if ref == "" && src.RefKind != "branch" {
		view := e.viewOf(src)
		return SkillSourceReport{Source: view, Added: []string{}, Removed: []string{}, Updated: []string{}, Pinned: true,
			Message: fmt.Sprintf("%s is pinned to %s %s (%s) — nothing to pull; `wfx skills ref %s <branch>` to follow a branch",
				name, src.RefKind, src.Ref, short(src.Commit), name)}, nil
	}
	if ref == "" {
		ref = src.Ref
	}
	dir := e.skillSourceDir(name)
	before := snapshotSkills(e.skillSourceRoot(src), name)
	env := e.gitCredentialEnv(ctx, src.URL)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// The checkout went missing (someone cleaned the work dir): rebuild it.
		_ = os.RemoveAll(dir)
		if err := initCheckout(ctx, dir, src.URL, src.Path); err != nil {
			return SkillSourceReport{}, err
		}
	}
	kind, commit, err := checkoutRef(ctx, dir, ref, env)
	if err == nil {
		_, err = checkSkillCheckout(dir, src.Path)
	}
	if err != nil {
		// Put the old commit back so a bad switch changes nothing.
		if src.Commit != "" {
			_, _ = gitIn(ctx, nil, "-C", dir, "checkout", "-q", "-f", "--detach", src.Commit)
		}
		return SkillSourceReport{}, err
	}
	src.Ref, src.RefKind, src.Commit, src.SyncedAt = ref, kind, commit, time.Now().UTC()
	src.Branch = ""
	if kind == "branch" {
		src.Branch = ref
	}
	list[i] = src
	if err := e.writeSkillSources(list); err != nil {
		return SkillSourceReport{}, err
	}
	if err := e.ReloadDefinitions(); err != nil {
		return SkillSourceReport{}, err
	}
	rep := diffReport(before, snapshotSkills(e.skillSourceRoot(src), name))
	rep.Source = e.viewOf(src)
	rep.Message = fmt.Sprintf("%s now at %s %s (%s): %d added, %d removed, %d updated",
		name, kind, ref, short(commit), len(rep.Added), len(rep.Removed), len(rep.Updated))
	return rep, nil
}

// RemoveSkillSource unloads a source's skills and deletes its checkout. Unlike
// a project clone, nobody edits a skill source's checkout by hand — it is the
// platform's copy of a remote — so deleting it is not destroying work.
func (e *Engine) RemoveSkillSource(name string) (SkillSourceReport, error) {
	skillSourceMu.Lock()
	defer skillSourceMu.Unlock()

	list, err := e.readSkillSources()
	if err != nil {
		return SkillSourceReport{}, err
	}
	i, err := findSkillSource(list, name)
	if err != nil {
		return SkillSourceReport{}, err
	}
	src := list[i]
	before := snapshotSkills(e.skillSourceRoot(src), name)
	view := e.viewOf(src)
	if err := e.writeSkillSources(append(list[:i:i], list[i+1:]...)); err != nil {
		return SkillSourceReport{}, err
	}
	if err := os.RemoveAll(e.skillSourceDir(name)); err != nil {
		return SkillSourceReport{}, err
	}
	if err := e.ReloadDefinitions(); err != nil {
		return SkillSourceReport{}, err
	}
	rep := diffReport(before, nil)
	rep.Source = view
	rep.Message = fmt.Sprintf("removed %s: %d skill(s) unloaded, checkout deleted", name, len(rep.Removed))
	return rep, nil
}

// SkillSourceSkills returns a source's skills with their SKILL.md text, for
// read-only display. Nothing here is executed or rendered as HTML server-side.
func (e *Engine) SkillSourceSkills(name string) ([]SkillFile, error) {
	list, err := e.readSkillSources()
	if err != nil {
		return nil, err
	}
	i, err := findSkillSource(list, name)
	if err != nil {
		return nil, err
	}
	root := e.skillSourceRoot(list[i])
	reg := e.Skills()
	var out []SkillFile
	for _, s := range skills.LoadRoots(skills.Root{Dir: root, Label: name}).List() {
		raw, _ := os.ReadFile(s.Location)
		if len(raw) > maxSkillContentBytes {
			raw = append(raw[:maxSkillContentBytes], []byte("\n\n… (truncated)")...)
		}
		rel, _ := filepath.Rel(e.skillSourceDir(name), s.Location)
		owner, ok := reg.Get(s.Name)
		out = append(out, SkillFile{Name: s.Name, Description: s.Description, Path: filepath.ToSlash(rel),
			Content: string(raw), Loaded: ok && owner.Location == s.Location})
	}
	return out, nil
}

// SkillSourceRefs lists the branches and tags the source's remote advertises,
// so a ref switch can be picked rather than typed.
func (e *Engine) SkillSourceRefs(ctx context.Context, name string) (RemoteRefs, error) {
	list, err := e.readSkillSources()
	if err != nil {
		return RemoteRefs{}, err
	}
	i, err := findSkillSource(list, name)
	if err != nil {
		return RemoteRefs{}, err
	}
	src := list[i]
	out, err := gitIn(ctx, e.gitCredentialEnv(ctx, src.URL), "ls-remote", "--heads", "--tags", "--", src.URL)
	if err != nil {
		return RemoteRefs{}, err
	}
	refs := RemoteRefs{Branches: []string{}, Tags: []string{}, Current: src.Ref}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		r := strings.TrimSuffix(f[1], "^{}")
		if seen[r] {
			continue
		}
		seen[r] = true
		switch {
		case strings.HasPrefix(r, "refs/heads/"):
			refs.Branches = append(refs.Branches, strings.TrimPrefix(r, "refs/heads/"))
		case strings.HasPrefix(r, "refs/tags/"):
			refs.Tags = append(refs.Tags, strings.TrimPrefix(r, "refs/tags/"))
		}
	}
	sort.Strings(refs.Branches)
	sort.Strings(refs.Tags)
	return refs, nil
}

// ---- git ---------------------------------------------------------------------

// initCheckout makes an empty repository with the remote set. `git init` makes
// no hooks we did not ship, and every later command runs with hooks pointed
// nowhere, so nothing from the remote ever runs.
//
// Only `path` is checked out (a sparse checkout), and fetches ask for blobs
// lazily, so importing the skills of a repository that also ships videos and
// binaries downloads the skills, not the videos.
func initCheckout(ctx context.Context, dir, url, path string) error {
	if _, err := gitIn(ctx, nil, "init", "-q", dir); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"core.symlinks", "false"}, {"core.hooksPath", os.DevNull},
		{"submodule.recurse", "false"}, {"advice.detachedHead", "false"}, {"core.sparseCheckout", "true"}} {
		if _, err := gitIn(ctx, nil, "-C", dir, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	pattern := "/*\n"
	if path != "." {
		pattern = "/" + path + "/\n"
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "sparse-checkout"), []byte(pattern), 0o644); err != nil {
		return err
	}
	_, err := gitIn(ctx, nil, "-C", dir, "remote", "add", "origin", url)
	return err
}

// fetchLazy fetches with blobs deferred to checkout where the server supports
// it, and plainly where it does not.
func fetchLazy(ctx context.Context, dir string, env []string, args ...string) error {
	base := []string{"-C", dir, "fetch", "-q"}
	if _, err := gitIn(ctx, env, append(append(base, "--filter=blob:none"), args...)...); err == nil {
		return nil
	}
	_, err := gitIn(ctx, env, append(base, args...)...)
	return err
}

// defaultBranch asks the remote which branch HEAD points at.
func defaultBranch(ctx context.Context, dir string, env []string) (string, error) {
	out, err := gitIn(ctx, env, "-C", dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ref: refs/heads/") {
			b := strings.Fields(strings.TrimPrefix(line, "ref: refs/heads/"))
			if len(b) > 0 {
				return b[0], nil
			}
		}
	}
	return "", fmt.Errorf("could not tell the remote's default branch — pass --ref")
}

// checkoutRef resolves ref against the remote (branch first, then tag, then a
// commit id), fetches exactly that, and checks it out detached.
func checkoutRef(ctx context.Context, dir, ref string, env []string) (kind, commit string, err error) {
	out, err := gitIn(ctx, env, "-C", dir, "ls-remote", "origin", "refs/heads/"+ref, "refs/tags/"+ref)
	if err != nil {
		return "", "", err
	}
	var fetch string
	switch {
	case strings.Contains(out, "\trefs/heads/"+ref+"\n") || strings.HasSuffix(out, "\trefs/heads/"+ref):
		kind, fetch = "branch", "refs/heads/"+ref
	case strings.Contains(out, "\trefs/tags/"+ref):
		kind, fetch = "tag", "refs/tags/"+ref
	case hexSHA.MatchString(ref):
		kind = "commit"
	default:
		return "", "", fmt.Errorf("the remote has no branch or tag named %q, and it is not a commit id", ref)
	}

	target := "FETCH_HEAD"
	switch {
	case fetch != "":
		err = fetchLazy(ctx, dir, env, "--depth", "1", "--no-tags", "origin", fetch)
	case len(ref) == 40:
		err = fetchLazy(ctx, dir, env, "--depth", "1", "--no-tags", "origin", ref)
	default:
		// A short id cannot be fetched by name; fetch the branches and tags
		// (commits and trees — blobs come at checkout, for one commit) and
		// find it among them.
		err = fetchLazy(ctx, dir, env, "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*")
		target = ref
	}
	if err != nil {
		return "", "", err
	}
	if kind == "commit" {
		full, rerr := gitIn(ctx, nil, "-C", dir, "rev-parse", "--verify", "-q", target+"^{commit}")
		if rerr != nil {
			return "", "", fmt.Errorf("commit %q was not found on the remote", ref)
		}
		target = strings.TrimSpace(full)
	}
	if _, err := gitIn(ctx, env, "-C", dir, "checkout", "-q", "-f", "--detach", target); err != nil {
		return "", "", err
	}
	// Anything the previous ref had and this one does not is untracked now.
	if _, err := gitIn(ctx, nil, "-C", dir, "clean", "-q", "-f", "-d", "-x"); err != nil {
		return "", "", err
	}
	head, err := gitIn(ctx, nil, "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return kind, strings.TrimSpace(head), nil
}

// gitCredentialEnv hands a GitHub token to git for a github.com URL, through
// GIT_CONFIG_* environment variables: never argv (visible in ps), never the
// URL (echoed in errors), never a log line. The token comes from the platform
// env store's system level: GH_TOKEN, else GITHUB_TOKEN.
func (e *Engine) gitCredentialEnv(ctx context.Context, url string) []string {
	if !strings.HasPrefix(strings.ToLower(url), "https://github.com/") {
		return nil
	}
	envs, err := e.platformEnv(ctx, "")
	if err != nil || envs == nil {
		return nil
	}
	tok := envs["GH_TOKEN"]
	if tok == "" {
		tok = envs["GITHUB_TOKEN"]
	}
	if tok == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}
}

// checkSkillCheckout enforces the bounds and that `path` is a directory inside
// the checkout, and returns how many SKILL.md files it holds.
func checkSkillCheckout(dir, path string) (int, error) {
	root := filepath.Join(dir, filepath.FromSlash(path))
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return 0, err
	}
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, fmt.Errorf("the repository has no %q directory at this ref", path)
	}
	if rel, err := filepath.Rel(absDir, absRoot); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("path %q leaves the repository", path)
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		return 0, fmt.Errorf("%q is not a directory in the repository", path)
	}
	var size int64
	count := 0
	err = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				size += info.Size()
			}
			if size > maxSkillSourceBytes {
				return fmt.Errorf("the checkout is over %d MB — a skills repository is text; point --path at the skills", maxSkillSourceBytes>>20)
			}
			if d.Name() == "SKILL.md" && strings.HasPrefix(p, absRoot) {
				count++
				if count > maxSkillsPerSource {
					return fmt.Errorf("more than %d skills under %q — narrow --path", maxSkillsPerSource, path)
				}
			}
		}
		return nil
	})
	return count, err
}

// ---- reporting ---------------------------------------------------------------

// snapshotSkills maps each skill under root to a hash of its SKILL.md, so a
// sync can say what changed and not only what appeared.
func snapshotSkills(root, label string) map[string]string {
	out := map[string]string{}
	if _, err := os.Stat(root); err != nil {
		return out
	}
	for _, s := range skills.LoadRoots(skills.Root{Dir: root, Label: label}).List() {
		raw, _ := os.ReadFile(s.Location)
		sum := sha256.Sum256(raw)
		out[s.Name] = hex.EncodeToString(sum[:])
	}
	return out
}

func diffReport(before, after map[string]string) SkillSourceReport {
	rep := SkillSourceReport{Added: []string{}, Removed: []string{}, Updated: []string{}}
	for n, h := range after {
		old, ok := before[n]
		switch {
		case !ok:
			rep.Added = append(rep.Added, n)
		case old != h:
			rep.Updated = append(rep.Updated, n)
		}
	}
	for n := range before {
		if _, ok := after[n]; !ok {
			rep.Removed = append(rep.Removed, n)
		}
	}
	sort.Strings(rep.Added)
	sort.Strings(rep.Removed)
	sort.Strings(rep.Updated)
	return rep
}

func (e *Engine) viewOf(s SkillSource) SkillSourceView {
	root := e.skillSourceRoot(s)
	v := SkillSourceView{SkillSource: s, Skills: []string{}}
	reg := e.Skills()
	for _, sk := range skills.LoadRoots(skills.Root{Dir: root, Label: s.Name}).List() {
		v.Skills = append(v.Skills, sk.Name)
		if reg == nil {
			continue
		}
		if owner, ok := reg.Get(sk.Name); ok && owner.Location != sk.Location {
			v.Clashes = append(v.Clashes, SkillClash{Skill: sk.Name, OwnedBy: owner.Source, Location: sk.Location})
		}
	}
	v.Count = len(v.Skills)
	return v
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
