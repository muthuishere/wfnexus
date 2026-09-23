package secrets

import (
	"strings"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	key, _ := NewKey()
	b, _, err := Load("WFX_TEST_KEY_ABSENT", "")
	if err == nil {
		t.Fatal("a box was built with no key at all")
	}
	t.Setenv("WFX_TEST_KEY", key)
	b, src, err := Load("WFX_TEST_KEY", "")
	if err != nil {
		t.Fatal(err)
	}
	if src != "WFX_TEST_KEY" {
		t.Fatalf("key source = %q", src)
	}
	sealed, err := b.Seal("ghp_realtoken")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "ghp_realtoken") {
		t.Fatal("the plaintext is still in the ciphertext")
	}
	got, err := b.Open(sealed)
	if err != nil || got != "ghp_realtoken" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

// The same value written twice must not produce the same bytes, or the table
// leaks which entries share a value.
func TestSealIsNotDeterministic(t *testing.T) {
	key, _ := NewKey()
	t.Setenv("WFX_TEST_KEY", key)
	b, _, _ := Load("WFX_TEST_KEY", "")
	a, _ := b.Seal("same")
	c, _ := b.Seal("same")
	if string(a) == string(c) {
		t.Fatal("sealing is deterministic; equal values are visibly equal in the database")
	}
}

// The wrong key must say it is the wrong key — a restored database with a fresh
// key file is the common case and "corrupt" would send someone the wrong way.
func TestWrongKeySaysSo(t *testing.T) {
	k1, _ := NewKey()
	k2, _ := NewKey()
	t.Setenv("K", k1)
	b1, _, _ := Load("K", "")
	sealed, _ := b1.Seal("v")
	t.Setenv("K", k2)
	b2, _, _ := Load("K", "")
	if _, err := b2.Open(sealed); err == nil || !strings.Contains(err.Error(), "wrong key") {
		t.Fatalf("err = %v", err)
	}
}

// A key file is written once and reused, so a restart does not orphan the store.
func TestKeyFileIsStableAcrossLoads(t *testing.T) {
	path := t.TempDir() + "/secret.key"
	b1, src, err := Load("WFX_TEST_KEY_ABSENT", path)
	if err != nil {
		t.Fatal(err)
	}
	if src != path {
		t.Fatalf("src = %q", src)
	}
	sealed, _ := b1.Seal("v")
	b2, _, err := Load("WFX_TEST_KEY_ABSENT", path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(sealed); err != nil || got != "v" {
		t.Fatalf("a restart could not read its own store: %q %v", got, err)
	}
}
