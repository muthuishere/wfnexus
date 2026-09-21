package engine

import (
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// broker fans persisted run events out to live SSE subscribers.
type broker struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan *store.Event]struct{}
}

func newBroker() *broker { return &broker{subs: map[uuid.UUID]map[chan *store.Event]struct{}{}} }

func (b *broker) Subscribe(runID uuid.UUID) (<-chan *store.Event, func()) {
	ch := make(chan *store.Event, 256)
	b.mu.Lock()
	if b.subs[runID] == nil {
		b.subs[runID] = map[chan *store.Event]struct{}{}
	}
	b.subs[runID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[runID], ch)
		b.mu.Unlock()
	}
}

func (b *broker) Publish(ev *store.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[ev.RunID] {
		select {
		case ch <- ev:
		default: // slow subscriber: drop, it will catch up from the DB cursor
		}
	}
}
