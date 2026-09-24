package bundle

import (
	"bytes"
	"context"
	"io"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
)

// Where the bytes go: the EXISTING blob store, keyed by content digest
// (design §5). No new storage layer — a laptop gets files under the Folder
// driver and a deployment gets objects in a bucket, and which one runs is
// already one line of config.
//
// Folder.pathFor refuses a key that escapes the directory; these keys are built
// from a hex digest, so none of them could.
func TarKey(digest string) string      { return "bundles/" + Hex(digest) + "/bundle.tar.gz" }
func ManifestKey(digest string) string { return "bundles/" + Hex(digest) + "/manifest.json" }

// Save writes the blobs. It is called BEFORE the index row is written: a blob
// with no index row is an orphan that GC (deferred) collects, where an index
// row with no blob is a broken bundle nobody can pull.
func Save(ctx context.Context, bs blob.Store, digest string, tarGz, manifest []byte) error {
	if err := bs.Put(ctx, TarKey(digest), bytes.NewReader(tarGz), int64(len(tarGz)), "application/gzip"); err != nil {
		return err
	}
	return bs.Put(ctx, ManifestKey(digest), bytes.NewReader(manifest), int64(len(manifest)), "application/json")
}

// Load reads a stored bundle back out of the blob store.
func Load(ctx context.Context, bs blob.Store, digest string) ([]byte, error) {
	rc, err := bs.Get(ctx, TarKey(digest))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
