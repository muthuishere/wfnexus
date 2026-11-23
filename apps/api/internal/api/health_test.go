package api

import (
	"encoding/json"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/buildinfo"
)

// /api/health says which build answered (#190), without a token: a deployment
// proves the release it runs by asking it.
func TestHealthReportsTheBuild(t *testing.T) {
	oldV, oldC := buildinfo.Version, buildinfo.Commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldV, oldC })
	buildinfo.Version, buildinfo.Commit = "v9.9.9", "abc1234"

	// Bound to every interface, so authentication applies: health stays public.
	h, _ := testServer(t, ":8090")
	res := do(h, "GET", "/api/health", "", nil)
	if res.Code != 200 {
		t.Fatalf("health: %d %s", res.Code, res.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", res.Body, err)
	}
	if got["ok"] != true || got["version"] != "v9.9.9" || got["commit"] != "abc1234" {
		t.Fatalf("health = %v, want ok, version v9.9.9, commit abc1234", got)
	}
}
