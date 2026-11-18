package engine

import (
	"testing"

	"github.com/google/uuid"
)

// When a run ends its redactor leaves the cache; its secrets stay, so a step
// that still writes (a cancelled run, a parallel branch) is still scrubbed.
func TestARunsRedactorLeavesTheCacheWhenItEnds(t *testing.T) {
	var r runSecrets
	id := uuid.New()
	r.add(id, map[string]string{"API_TOKEN": "cache-value-0123456"}, map[string]bool{"API_TOKEN": true})
	_ = redactText("x", r.get(id))
	key := redactorKey(r.get(id))
	r.dropRedactor(id)
	redactorMu.Lock()
	_, cached := redactorCache[key]
	redactorMu.Unlock()
	if cached || len(r.get(id)) != 1 {
		t.Fatalf("cached %v, secrets %d", cached, len(r.get(id)))
	}
	// The cache is capped.
	for i := 0; i < 2*redactorCacheMax; i++ {
		_ = redactorFor([]secretValue{{"K", uuid.NewString()}})
	}
	redactorMu.Lock()
	n := len(redactorCache)
	redactorMu.Unlock()
	if n > redactorCacheMax {
		t.Fatalf("cache holds %d sets", n)
	}
}
