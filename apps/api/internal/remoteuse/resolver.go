// Package remoteuse resolves a remote `use:` — a git repository and a ref —
// into the task a workflow expands, and keeps what it fetched in the EXISTING
// blob store.
//
// Git is the transport, shelled out to exactly as engine/sources.go already
// does for imported repositories. That is not laziness: `insteadOf`, the
// credential helper, the SSH agent and every mirror an operator already
// configured come for free, and a library would have to re-implement all of
// them. wfnexus never reads, stores or prompts for a git credential.
//
// The blob store stops being *the place bundles live* and becomes *the place
// fetched bundles are kept*. That demotion is only safe because the content is
// addressed by digest: a cache that can be deleted at any time without changing
// what a workflow means is a cache; one that cannot is a store.
package remoteuse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// fetchTimeout bounds one git conversation. An unreachable remote must become
// a load error, not a boot that never finishes.
const fetchTimeout = 2 * time.Minute

// Resolver implements workflow.RemoteResolver.
type Resolver struct {
	blob    blob.Store
	rootDir string // where a fetched bundle's skills are materialised

	// offline refuses to start git at all. Set by the caller for an air-gapped
	// host; a warm cache still resolves, a cold one fails naming the cache.
	offline bool

	mu    sync.Mutex
	roots []string
	// fetched memoises within one process, so a load that names the same
	// reference twice runs git once.
	fetched map[string]resolution
}

type resolution struct {
	Commit string `json:"commit"`
	Digest string `json:"digest"`
}

// New builds a resolver over the platform's blob store. rootDir is the engine's
// BundleRootDir: one directory per bundle, prepended to the skill roots, which
// is exactly what `wfx pull` already does (engine.PrependSkillRoot).
func New(bs blob.Store, rootDir string) *Resolver {
	return &Resolver{blob: bs, rootDir: rootDir, fetched: map[string]resolution{}}
}

// Offline makes every cold-cache resolution a refusal instead of a fetch.
func (r *Resolver) Offline(v bool) *Resolver { r.offline = v; return r }

// Roots is every bundle-scoped skill root this resolver has materialised, most
// recent first. The engine prepends them so skills.Load's first-root-wins
// resolves a step's `skills:` to the carried copy.
func (r *Resolver) Roots() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.roots...)
}

// refKey is the cache's name→content index. Hex, so no reference text can ever
// shape a blob key — blob.Folder refuses a key that escapes its directory, and
// a hex digest could not.
func refKey(ref workflow.Ref) string {
	sum := sha256.Sum256([]byte(ref.Remote + "\x00" + ref.Ref + "\x00" + ref.Bundle))
	return "refs/" + hex.EncodeToString(sum[:]) + ".json"
}

// cacheLocation is what the refusal names. A store that cannot say where it
// looked is a store nobody can debug.
func (r *Resolver) cacheLocation() string {
	if d, ok := r.blob.(interface{ Dir() string }); ok {
		return d.Dir()
	}
	return "the configured artifact store"
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ResolveRemoteUse is the whole contract: cache first, git second, refuse
// third. The COMMIT is recorded, never the tag — a tag can move and a commit
// cannot, which is the only reason a rerun can be exact.
func (r *Resolver) ResolveRemoteUse(ref workflow.Ref) (*workflow.Task, workflow.RemotePin, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	key := refKey(ref)
	r.mu.Lock()
	res, warm := r.fetched[key]
	r.mu.Unlock()
	if !warm {
		if got, err := r.readRef(ctx, key); err == nil {
			res, warm = got, true
		}
	}
	if warm {
		// A CACHE HIT MAKES NO NETWORK CALL. The bytes are content-addressed,
		// so a hit is the same answer the remote would have given.
		t, err := r.fromCache(ctx, res.Digest)
		if err == nil {
			return t, workflow.RemotePin{Reference: ref.Raw, Remote: ref.Remote, Commit: res.Commit, Digest: res.Digest}, nil
		}
		// The index knew a digest the store no longer holds. Fall through and
		// refetch; deleting the cache must change timing and nothing else.
	}

	if r.offline {
		return nil, workflow.RemotePin{}, fmt.Errorf(
			"this host is offline and the cache does not hold %q from %s (looked for %s in %s)",
			ref.Raw, ref.Remote, key, r.cacheLocation())
	}

	commit, dir, cleanup, err := r.fetch(ctx, ref)
	if err != nil {
		return nil, workflow.RemotePin{}, fmt.Errorf("%w (the cache in %s does not hold it either)", err, r.cacheLocation())
	}
	defer cleanup()

	bundleDir, err := bundle.SelectInTree(dir, ref.Bundle)
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	bun, err := bundle.FromTree(bundleDir)
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	// The same recomputation the upload path runs. A checkout is not more
	// trusted than an upload just because git carried it.
	digest, err := bun.Verify("")
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	tarGz, err := bun.Pack()
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	canon, err := bun.Manifest.Canonical()
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	if err := bundle.Save(ctx, r.blob, digest, tarGz, canon); err != nil {
		return nil, workflow.RemotePin{}, err
	}
	if err := r.writeRef(ctx, key, resolution{Commit: commit, Digest: digest}); err != nil {
		return nil, workflow.RemotePin{}, err
	}
	r.mu.Lock()
	r.fetched[key] = resolution{Commit: commit, Digest: digest}
	r.mu.Unlock()

	t, err := r.install(bun, digest)
	if err != nil {
		return nil, workflow.RemotePin{}, err
	}
	return t, workflow.RemotePin{Reference: ref.Raw, Remote: ref.Remote, Commit: commit, Digest: digest}, nil
}

// fromCache rebuilds the task from stored bytes — no git, no network.
func (r *Resolver) fromCache(ctx context.Context, digest string) (*workflow.Task, error) {
	raw, err := bundle.Load(ctx, r.blob, digest)
	if err != nil {
		return nil, err
	}
	bun, err := bundle.Unpack(raw)
	if err != nil {
		return nil, err
	}
	if _, err := bun.Verify(digest); err != nil {
		return nil, err
	}
	return r.install(bun, digest)
}

// install materialises the carried skills as a bundle-scoped skill root and
// turns the carried workflow into the task the `use:` expands.
func (r *Resolver) install(bun *bundle.Bundle, digest string) (*workflow.Task, error) {
	if r.rootDir != "" {
		root := filepath.Join(r.rootDir, bundle.Hex(digest))
		if err := bun.Materialise(root); err != nil {
			return nil, err
		}
		r.mu.Lock()
		seen := false
		for _, e := range r.roots {
			if e == root {
				seen = true
			}
		}
		if !seen {
			r.roots = append([]string{root}, r.roots...)
		}
		r.mu.Unlock()
	}
	return taskFrom(bun)
}

// taskFrom reads the bundle's workflow as a TASK: a named bundle of steps. The
// steps then walk the same expansion a local task's do.
func taskFrom(bun *bundle.Bundle) (*workflow.Task, error) {
	raw, ok := bun.Files[bundle.WorkflowPath]
	if !ok {
		return nil, errors.New("bundle carries no " + bundle.WorkflowPath)
	}
	def, err := workflow.ParseYAML(raw)
	if err != nil {
		return nil, err
	}
	// TRANSITIVE remote references are not supported, and a half-resolved one
	// is worse than a refusal. Named, refused, nothing fetched.
	for _, u := range def.Uses {
		if workflow.IsRemoteUse(u.Task) {
			return nil, fmt.Errorf("the fetched bundle %q itself declares the remote use %q: "+
				"transitive remote references are not supported", bun.Manifest.Name, u.Task)
		}
	}
	if len(def.Steps) == 0 {
		return nil, fmt.Errorf("the fetched bundle %q carries no steps to expand", bun.Manifest.Name)
	}
	name := bun.Manifest.Name
	if name == "" {
		name = def.Name
	}
	return &workflow.Task{Name: name, Steps: def.Steps, Description: def.Description}, nil
}

func (r *Resolver) readRef(ctx context.Context, key string) (resolution, error) {
	rc, err := r.blob.Get(ctx, key)
	if err != nil {
		return resolution{}, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 4<<10))
	if err != nil {
		return resolution{}, err
	}
	var out resolution
	if err := json.Unmarshal(raw, &out); err != nil {
		return resolution{}, err
	}
	if out.Digest == "" || out.Commit == "" {
		return resolution{}, errors.New("incomplete cache index entry")
	}
	return out, nil
}

func (r *Resolver) writeRef(ctx context.Context, key string, res resolution) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return r.blob.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), "application/json")
}

// fetch clones the one ref into a temporary checkout and reports the COMMIT it
// landed on. Shallow and single-ref, because a bundle is a small directory in
// what may be a large repository.
func (r *Resolver) fetch(ctx context.Context, ref workflow.Ref) (commit, dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "wfx-use-")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	fail := func(step string, out []byte, e error) (string, string, func(), error) {
		cleanup()
		return "", "", func() {}, fmt.Errorf("git %s for %q from %s: %v: %s",
			step, ref.Raw, ref.Remote, e, strings.TrimSpace(string(out)))
	}
	git := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	}
	if out, e := exec.CommandContext(ctx, "git", "init", "-q", dir).CombinedOutput(); e != nil {
		return fail("init", out, e)
	}
	if out, e := git("remote", "add", "origin", ref.Remote); e != nil {
		return fail("remote add", out, e)
	}
	// One ref, depth 1 — the usual case, and the cheap one.
	out, e := git("fetch", "--depth", "1", "--quiet", "origin", ref.Ref)
	if e != nil {
		// A server that refuses a bare SHA in a want, or a ref spelled as a
		// remote branch, gets the ordinary fetch.
		if out2, e2 := git("fetch", "--quiet", "--tags", "origin",
			"+refs/heads/*:refs/remotes/origin/*"); e2 != nil {
			return fail("fetch", append(out, out2...), e)
		}
	}
	checkout := "FETCH_HEAD"
	if e != nil {
		checkout = ref.Ref
		if _, bad := git("rev-parse", "--verify", "--quiet", checkout+"^{commit}"); bad != nil {
			checkout = "origin/" + ref.Ref
		}
	}
	if out, e := git("checkout", "--quiet", "--detach", checkout); e != nil {
		return fail("checkout "+checkout, out, e)
	}
	sha, e := git("rev-parse", "HEAD")
	if e != nil {
		return fail("rev-parse", sha, e)
	}
	commit = strings.TrimSpace(string(sha))
	if !shaRe.MatchString(commit) {
		return fail("rev-parse", sha, errors.New("no commit"))
	}
	return commit, dir, cleanup, nil
}

var _ workflow.RemoteResolver = (*Resolver)(nil)
