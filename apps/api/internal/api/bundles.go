package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The publish direction (ADR 0018).
//
// PUBLISHING IS ABSENT, NOT DEGRADED, where there is no subject to record. A
// loopback install has no users by construction (ADR 0017), so there is nobody
// to attribute a publish to and these routes are NOT MOUNTED at all: the path
// 404s, the same as any other route this build does not have. That is the
// difference between a capability that is missing and one that is merely
// switched off — the second kind comes back when somebody flips a setting.
//
// The other condition is a place to put the bytes: no blob store, no registry,
// no publishing route.
func (s *Server) publishingEnabled() bool { return s.blob != nil && !s.loopbackOnly }

// publishBody is what `wfx publish` sends. The bundle is the tar; the digest is
// the client's CLAIM about it, which the server recomputes and never trusts.
//
// Nothing here names the publisher. Provenance comes from the authenticated
// subject, so a body cannot attribute a publish to somebody else.
type publishBody struct {
	Kind    string `json:"kind"`
	Project string `json:"project"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	TarGz   []byte `json:"tarGz"`
	Tag     string `json:"tag,omitempty"`
}

func (s *Server) publishBundle(w http.ResponseWriter, r *http.Request) {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser {
		writeErr(w, http.StatusUnauthorized, errors.New("publishing requires an authenticated user"))
		return
	}
	var b publishBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.Name == "" || b.Version == "" {
		writeErr(w, 400, errors.New("`name` and `version` are required"))
		return
	}
	if b.Kind == "" {
		b.Kind = "workflow"
	}
	if sub.Project != "" && b.Project != sub.Project {
		writeErr(w, http.StatusForbidden, fmt.Errorf("refused: this subject is scoped to project %q", sub.Project))
		return
	}

	bun, err := bundle.Unpack(b.TarGz)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	// The server recomputes EVERY entry digest and the manifest digest from the
	// bytes it received. Content addressing is worth nothing if the store
	// accepts bytes on the uploader's word.
	digest, err := bun.Verify(b.Digest)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if bun.Manifest.Name != b.Name || bun.Manifest.Version != b.Version || bun.Manifest.Project != b.Project {
		writeErr(w, 400, errors.New("the manifest does not name the same project/name/version as the request"))
		return
	}

	// The credential check again, SERVER-SIDE. The client ran it so nothing
	// left the machine; the server runs it because a client is not a guard.
	def, err := workflow.ParseYAML(bun.Files[bundle.WorkflowPath])
	if err != nil {
		writeErr(w, 400, fmt.Errorf("bundle workflow.yaml: %w", err))
		return
	}
	if err := bundle.CheckNoLiteralCredential(def, nil, nil, nil); err != nil {
		// The message names the field and never the value.
		writeErr(w, 400, err)
		return
	}

	// Immutability: a version that exists is a REFUSAL, not an overwrite, even
	// for byte-identical content. The message says which it was, so the author
	// knows whether they are looking at a no-op or a conflict.
	if existing, err := s.store.Bundle(r.Context(), b.Project, b.Kind, b.Name, b.Version); err == nil {
		same := "a different digest"
		if existing.Digest == digest {
			same = "the same digest — this is a no-op, not a conflict"
		}
		writeErr(w, http.StatusConflict, fmt.Errorf("%s@%s is already published with %s (%s); publish a new version",
			b.Name, b.Version, existing.Digest, same))
		return
	}

	manifest, err := bun.Manifest.Canonical()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Blobs first, then the index row: a blob with no index row is an orphan
	// that GC (deferred) collects, where an index row with no blob is a broken
	// bundle nobody can pull.
	if err := bundle.Save(r.Context(), s.blob, digest, b.TarGz, manifest); err != nil {
		writeErr(w, 500, err)
		return
	}
	row := &store.PublishedBundle{
		Kind: b.Kind, Project: b.Project, Name: b.Name, Version: b.Version,
		Digest: digest, Manifest: manifest,
		PublishedBy: &sub.User.ID, PublishedByName: sub.Name,
	}
	if err := s.store.CreateBundle(r.Context(), row); err != nil {
		if errors.Is(err, store.ErrVersionExists) {
			writeErr(w, http.StatusConflict, fmt.Errorf("%s@%s is already published", b.Name, b.Version))
			return
		}
		writeErr(w, 500, err)
		return
	}
	if b.Tag != "" {
		if err := s.store.SetBundleTag(r.Context(), b.Project, b.Kind, b.Name, b.Tag, b.Version); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	writeJSON(w, 201, row)
}

func (s *Server) listBundles(w http.ResponseWriter, r *http.Request) {
	f := store.BundleFilter{
		Project: r.URL.Query().Get("project"),
		Kind:    r.URL.Query().Get("kind"),
		Name:    r.URL.Query().Get("name"),
	}
	// Filtered, not merely checked: a scoped subject sees only its own project.
	if scope := s.scopeOf(r); scope != "" {
		f.Project = scope
	}
	out, err := s.store.ListBundles(r.Context(), f)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, out)
}

// bundleInScope loads a bundle by digest and refuses one outside the caller's
// project scope, indistinguishably from one that does not exist.
func (s *Server) bundleInScope(w http.ResponseWriter, r *http.Request) (*store.PublishedBundle, bool) {
	row, err := s.store.BundleByDigest(r.Context(), chi.URLParam(r, "digest"))
	if err != nil || !s.inScope(r, row.Project) {
		notFound(w)
		return nil, false
	}
	return row, true
}

// getBundle is `inspect`: the manifest and the provenance, as queryable fields.
func (s *Server) getBundle(w http.ResponseWriter, r *http.Request) {
	row, ok := s.bundleInScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, row)
}

// getBundleTar hands the bytes back — what `wfx pull` fetches.
func (s *Server) getBundleTar(w http.ResponseWriter, r *http.Request) {
	row, ok := s.bundleInScope(w, r)
	if !ok {
		return
	}
	raw, err := bundle.Load(r.Context(), s.blob, row.Digest)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

// installBundle materialises a published bundle ON THIS SERVER so it can be
// run: the carried skills become a BUNDLE-SCOPED SKILL ROOT which is prepended
// to the machine's own roots, and the workflow is saved.
//
// skills.Load's first-root-wins then resolves each step's `skills:` to the
// carried copy, and a same-named machine skill is recorded in Shadowed rather
// than silently winning. The resolution rule is used, not bypassed.
//
// This is server work on a CLI-initiated pull. No platform tool is added and
// none of this is reachable from a step (skills/platform.go).
func (s *Server) installBundle(w http.ResponseWriter, r *http.Request) {
	row, ok := s.bundleInScope(w, r)
	if !ok {
		return
	}
	raw, err := bundle.Load(r.Context(), s.blob, row.Digest)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	bun, err := bundle.Unpack(raw)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if _, err := bun.Verify(row.Digest); err != nil {
		writeErr(w, 500, err)
		return
	}
	root := filepath.Join(s.eng.BundleRootDir(), bundle.Hex(row.Digest))
	if err := bun.Materialise(root); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := s.eng.PrependSkillRoot(root); err != nil {
		writeErr(w, 500, err)
		return
	}
	def, err := workflow.ParseYAML(bun.Files[bundle.WorkflowPath])
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if _, err := s.eng.SaveWorkflow(def); err != nil {
		writeErr(w, 400, err)
		return
	}
	// Which carried skills SHADOW a same-named local one, named with both
	// digests, so a pull never silently prefers either.
	writeJSON(w, 200, map[string]any{
		"ok": true, "workflow": def.Name, "skillRoot": root,
		"skills": bun.Manifest.Skills,
	})
}

// setBundleTag moves a tag. A tag moves; a version never does.
func (s *Server) setBundleTag(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Project string `json:"project"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.Kind == "" {
		b.Kind = "workflow"
	}
	if !s.inScope(r, b.Project) {
		notFound(w)
		return
	}
	if _, err := s.store.Bundle(r.Context(), b.Project, b.Kind, b.Name, b.Version); err != nil {
		notFound(w)
		return
	}
	tag := chi.URLParam(r, "tag")
	if err := s.store.SetBundleTag(r.Context(), b.Project, b.Kind, b.Name, tag, b.Version); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tag": tag, "version": b.Version})
}
