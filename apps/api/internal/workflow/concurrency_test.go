package workflow

import (
	"strings"
	"testing"
)

func TestConcurrencyParses(t *testing.T) {
	cases := map[string]Concurrency{
		"concurrency: 1\n": {},
		"concurrency:\n  group: db\n  cancel-in-progress: true\n": {Group: "db", CancelInProgress: true},
		"concurrency:\n  cancel-in-progress: false\n":             {},
	}
	for src, want := range cases {
		d, err := DecodeYAMLStrict([]byte("name: w\n" + src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if d.Concurrency == nil || *d.Concurrency != want {
			t.Fatalf("%q: got %+v", src, d.Concurrency)
		}
	}
	d, _ := DecodeYAMLStrict([]byte("name: w\n"))
	if d.Concurrency != nil || d.Concurrency.GroupFor("w") != "" {
		t.Fatal("absent concurrency must mean none")
	}
	if (&Concurrency{}).GroupFor("w") != "w" {
		t.Fatal("group defaults to the workflow name")
	}
}

func TestConcurrencyRefusesUnknownKeysAndBadShorthand(t *testing.T) {
	for _, src := range []string{
		"concurrency:\n  group: db\n  cancel_in_progres: true\n",
		"concurrency: 2\n",
		"concurrency: yes-please\n",
	} {
		if _, err := DecodeYAMLStrict([]byte("name: w\n" + src)); err == nil || !strings.Contains(err.Error(), "concurrency") {
			t.Fatalf("%q: want a concurrency error, got %v", src, err)
		}
	}
}

func TestConcurrencyJSONDialect(t *testing.T) {
	d, err := DecodeDefinition([]byte(`{"name":"w","concurrency":{"group":"g","cancel-in-progress":true}}`))
	if err != nil || d.Concurrency == nil || *d.Concurrency != (Concurrency{Group: "g", CancelInProgress: true}) {
		t.Fatalf("got %+v, %v", d.Concurrency, err)
	}
	d, err = DecodeDefinition([]byte(`{"name":"w","concurrency":1}`))
	if err != nil || d.Concurrency == nil {
		t.Fatalf("shorthand: %+v, %v", d, err)
	}
	if _, err := DecodeDefinition([]byte(`{"name":"w","concurrency":{"bogus":1}}`)); err == nil {
		t.Fatal("unknown key accepted")
	}
}
