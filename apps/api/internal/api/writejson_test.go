package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// A response that cannot be encoded must never leave as a 200 with no body.
//
// The original writeJSON committed to the status code first and then discarded
// the encoder's error, so one unencodable field answered "200 OK, 0 bytes" —
// indistinguishable, to a client, from a successful request, and invisible in
// the server log. A caller doing `json.load(curl ...)` saw only "Expecting
// value: line 1 column 1".
func TestWriteJSONNeverAnswers200WithNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	// json.RawMessage that is non-nil but empty is not valid JSON — the exact
	// shape a blank json column used to produce.
	writeJSON(rec, 200, map[string]any{"run": json.RawMessage{}})

	res := rec.Result()
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatalf("an unencodable response was answered with %d and a zero-byte body", res.StatusCode)
	}
	if res.StatusCode != 500 {
		t.Fatalf("status = %d, want 500 — the request failed and must say so", res.StatusCode)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("the failure response is not JSON: %v (%q)", err, body)
	}
	if v["error"] == nil {
		t.Fatalf("no error in the failure response: %q", body)
	}
}

// The ordinary path still answers exactly what it was given, with a length.
func TestWriteJSONHappyPath(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 201, map[string]any{"ok": true})
	res := rec.Result()
	if res.StatusCode != 201 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Length"); got != "12" {
		t.Fatalf("content-length = %q, want 12 for %q", got, rec.Body.Bytes())
	}
	var v struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || !v.OK {
		t.Fatalf("body = %q err=%v", rec.Body.Bytes(), err)
	}
}
