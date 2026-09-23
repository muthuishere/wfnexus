package workflow

import (
	"strings"
	"testing"
)

// A reference is resolved against the machine that runs the step; a variable
// nobody set is an error NAMING it, because the alternative is an empty string
// and a 401 somewhere far away that says nothing about the cause.
func TestResolveEnv(t *testing.T) {
	t.Setenv("WFX_TEST_TOKEN", "s3cret")
	got, err := ResolveEnv(map[string]string{"LITERAL": "plain", "TOK": "${WFX_TEST_TOKEN}", "MIX": "Bearer $WFX_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LITERAL=plain", "MIX=Bearer s3cret", "TOK=s3cret"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}

	if _, err := ResolveEnv(map[string]string{"A": "${WFX_TEST_ABSENT}"}); err == nil {
		t.Fatal("a missing variable resolved to empty rather than erroring")
	} else if !strings.Contains(err.Error(), "WFX_TEST_ABSENT") {
		t.Fatalf("the error does not name the variable: %v", err)
	}
}

// THE VALUE MUST NEVER BE RENDERED. The agent's bash tool has no env argument,
// so assignments go in front of the command — and that command is emitted to
// the run's event log. A reference therefore stays a reference, expanded by the
// child shell out of what it already inherited.
func TestShellPrefixNeverRendersASecret(t *testing.T) {
	t.Setenv("WFX_TEST_TOKEN", "s3cret")
	got := ShellPrefix(map[string]string{"GH_TOKEN": "${WFX_TEST_TOKEN}", "NODE_ENV": "test"})
	if strings.Contains(got, "s3cret") {
		t.Fatalf("the prefix contains the VALUE, which would be logged: %q", got)
	}
	if !strings.Contains(got, `GH_TOKEN="${WFX_TEST_TOKEN}"`) {
		t.Fatalf("the reference was not preserved for the shell to expand: %q", got)
	}
	if !strings.Contains(got, "NODE_ENV='test'") {
		t.Fatalf("a literal should be quoted as itself: %q", got)
	}
}

// A single quote in a literal must not end the quoting.
func TestShellPrefixQuotesAwkwardLiterals(t *testing.T) {
	got := ShellPrefix(map[string]string{"MSG": "it's fine"})
	// The POSIX idiom: close the quote, an escaped quote, reopen.
	if got != `MSG='it'\''s fine' ` {
		t.Fatalf("bad quoting: %q", got)
	}
}

// EnvKeys reports names, never values — it is what the dry run prints.
func TestEnvKeysAreNamesOnly(t *testing.T) {
	keys := EnvKeys(map[string]string{"A": "${FOO}", "B": "literal", "C": "$BAR and ${FOO}"})
	if strings.Join(keys, ",") != "BAR,FOO" {
		t.Fatalf("got %v", keys)
	}
}

// A credential written as a literal is refused: the file is committed, so
// saving it is the leak, and it looks like ordinary configuration.
func TestCheckEnvRefusesALiteralCredential(t *testing.T) {
	if err := CheckEnv("wf", map[string]string{"GITHUB_TOKEN": "ghp_realvalue"}); err == nil {
		t.Fatal("a literal token was accepted into a committed file")
	}
	for _, ok := range []map[string]string{
		{"GITHUB_TOKEN": "${GITHUB_PAT}"}, // named, not written
		{"NODE_ENV": "production"},        // not a credential
	} {
		if err := CheckEnv("wf", ok); err != nil {
			t.Errorf("%v was refused: %v", ok, err)
		}
	}
}
