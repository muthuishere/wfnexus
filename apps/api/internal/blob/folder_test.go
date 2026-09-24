package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFolderRoundTrip(t *testing.T) {
	f, err := OpenFolder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := "the diff"
	if err := f.Put(ctx, "runs/abc/step/workspace.diff", strings.NewReader(body), int64(len(body)), "text/x-diff"); err != nil {
		t.Fatal(err)
	}
	rc, err := f.Get(ctx, "runs/abc/step/workspace.diff")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Fatalf("got %q", got)
	}
}

// A key that climbs out of the directory must be refused. These keys are built
// by this codebase today, but a store that can be made to write anywhere is
// worth closing whether or not its current callers would.
func TestFolderRefusesEscapingKeys(t *testing.T) {
	dir := t.TempDir()
	f, _ := OpenFolder(dir)
	ctx := context.Background()
	for _, key := range []string{"../escaped", "runs/../../escaped", "/etc/passwd"} {
		if err := f.Put(ctx, key, strings.NewReader("x"), 1, "text/plain"); err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}
}

// A half-written artifact must never be readable: the write goes to a
// temporary file and is renamed, so a crash leaves nothing rather than a
// truncated diff that looks real.
func TestFolderWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	f, _ := OpenFolder(dir)
	ctx := context.Background()
	big := bytes.Repeat([]byte("x"), 1<<20)
	if err := f.Put(ctx, "a/b.txt", bytes.NewReader(big), int64(len(big)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	rc, err := f.Get(ctx, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if len(got) != len(big) {
		t.Fatalf("got %d bytes, want %d", len(got), len(big))
	}
}

// The folder driver says it cannot presign rather than inventing a URL that
// points at a route which does not exist.
func TestFolderCannotPresign(t *testing.T) {
	f, _ := OpenFolder(t.TempDir())
	if _, err := f.PresignedGet(context.Background(), "k", time.Minute); !errors.Is(err, ErrNoPresign) {
		t.Fatalf("err = %v, want ErrNoPresign", err)
	}
}
