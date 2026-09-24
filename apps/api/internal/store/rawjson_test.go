package store

import (
	"context"
	"encoding/json"
	"testing"
)

// A json column that holds the empty string used to come back as a NON-NIL,
// zero-length json.RawMessage — which is not valid JSON, so encoding the Run
// that carried it failed. In the API that failure was discarded, and the
// request answered 200 with a zero-byte body and no log line.
//
// SQLite is where this bites: the driver hands a text column back as a Go
// string, and converting an empty string to []byte yields an empty slice that
// is not nil. The []byte path (Postgres) already normalised to nil.
func TestRawJSONEmptyStringScansAsNull(t *testing.T) {
	for _, src := range []any{"", []byte{}, nil, "   "} {
		var dst json.RawMessage
		if err := (rawJSON{&dst}).Scan(src); err != nil {
			t.Fatalf("scan %#v: %v", src, err)
		}
		if dst != nil {
			t.Fatalf("scan %#v left a non-nil empty RawMessage %#v", src, dst)
		}
		if _, err := json.Marshal(map[string]any{"v": dst}); err != nil {
			t.Fatalf("scan %#v produced a value that cannot be encoded: %v", src, err)
		}
	}
}

// The same thing through a real SQLite file: a run whose input column is ”
// must still encode.
func TestRunWithEmptyJSONColumnStillEncodes(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	run, err := st.CreateRun(ctx, "demo", "hello", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE workflow_runs SET input='' WHERE id=?`, run.ID.String()); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("a run read back from the store could not be encoded: %v", err)
	}
}
