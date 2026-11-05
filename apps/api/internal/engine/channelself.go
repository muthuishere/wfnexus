package engine

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// selfRetry is how long a failed (or empty) `self` answer is remembered
// before the adapter is asked again.
const selfRetry = 5 * time.Minute

type selfEntry struct {
	ids []string
	at  time.Time
}

type selfCache struct {
	mu sync.Mutex
	m  map[string]selfEntry
}

// selfIDs is who WE are on a channel, for group mention/reply activation
// (ADR 0026): the registry entry's `self` list when it has one, else what the
// adapter's optional `self` verb reports (cached per channel and run command).
// Empty means unknown: a mention-activated group subscription then starts
// nothing, and its check says so.
func (e *Engine) selfIDs(ctx context.Context, c catalog.Channel) []string {
	if len(c.Self) > 0 {
		return c.Self
	}
	key := c.Name + "\x00" + c.Run
	e.self.mu.Lock()
	if ent, ok := e.self.m[key]; ok && (len(ent.ids) > 0 || time.Since(ent.at) < selfRetry) {
		e.self.mu.Unlock()
		return ent.ids
	}
	e.self.mu.Unlock()
	ids, err := e.adapter(c).Self(ctx)
	if err != nil {
		log.Printf("channel %s: no `self` list and the adapter could not report one: %v", c.Name, adapterErr(err))
		ids = nil
	}
	e.self.mu.Lock()
	if e.self.m == nil {
		e.self.m = map[string]selfEntry{}
	}
	e.self.m[key] = selfEntry{ids: ids, at: time.Now()}
	e.self.mu.Unlock()
	return ids
}
