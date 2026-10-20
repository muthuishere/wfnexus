package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// Over the wfx SSH tunnel every byte and every re-download costs RTTs, so the
// hashed bundle must be cacheable forever and index.html must not be.
func TestUICacheHeaders(t *testing.T) {
	s := &Server{uiFS: fstest.MapFS{
		"index.html":          {Data: []byte("<html></html>")},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
	}}
	cases := []struct{ path, want string }{
		{"/assets/index-abc.js", "public, max-age=31536000, immutable"},
		{"/", "no-cache"},
		{"/projects/x", "no-cache"},           // SPA fallback
		{"/assets/index-gone.js", "no-cache"}, // a stale hash falls back to index.html: never immutable
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.ui(rec, httptest.NewRequest("GET", c.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.want)
		}
	}
}

func TestAPIResponsesAreGzipped(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:0")
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", got)
	}
}
