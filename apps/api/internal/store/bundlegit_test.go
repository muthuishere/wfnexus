package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// task 7.2 — an indexed bundle that came from git records the remote and the
// RESOLVED COMMIT, so the git view and this index can be reconciled; one that
// was uploaded straight to this host records NEITHER, as absent and not as an
// empty string pretending to be a value.
func TestABundleFromGitRecordsItsRemoteAndCommit(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()
	manifest := json.RawMessage(`{"name":"code-review"}`)

	remote, commit := "ssh://git@nas.lan/srv/wf.git", "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	fromGit := &PublishedBundle{
		Kind: "workflow", Name: "code-review", Version: "1.0.0",
		Digest: "sha256:aa", Manifest: manifest,
		GitRemote: &remote, GitCommit: &commit,
	}
	if err := st.CreateBundle(ctx, fromGit); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.Bundle(ctx, "", "workflow", "code-review", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.FromGit() || *got.GitRemote != remote || *got.GitCommit != commit {
		t.Fatalf("the git origin did not round trip: %+v", got)
	}

	uploaded := &PublishedBundle{
		Kind: "workflow", Name: "code-review", Version: "2.0.0",
		Digest: "sha256:bb", Manifest: manifest,
	}
	if err := st.CreateBundle(ctx, uploaded); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err = st.Bundle(ctx, "", "workflow", "code-review", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.FromGit() || got.GitRemote != nil || got.GitCommit != nil {
		t.Fatalf("a direct upload came back with a git origin: %+v", got)
	}
	// And it is absent from the JSON, not present and empty — a consumer must
	// not be able to read "" as a remote.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gitRemote", "gitCommit"} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("%s is present for a direct upload: %s", k, raw)
		}
	}
}

// A remote with no commit names a place without naming what was read from it,
// so the two views could never be joined. Refused, on both dialects, by the
// store — SQLite cannot add a table CHECK to an existing table.
func TestAHalfGitOriginIsRefused(t *testing.T) {
	st := openSQLite(t)
	remote := "ssh://git@nas.lan/srv/wf.git"
	err := st.CreateBundle(context.Background(), &PublishedBundle{
		Kind: "workflow", Name: "half", Version: "1.0.0", Digest: "sha256:cc",
		Manifest: json.RawMessage(`{}`), GitRemote: &remote,
	})
	if !errors.Is(err, ErrGitOriginHalf) {
		t.Fatalf("a remote with no commit was accepted: %v", err)
	}
	if _, err := st.Bundle(context.Background(), "", "workflow", "half", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused insert left a row: %v", err)
	}
}
