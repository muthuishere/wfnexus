package store

import (
	"context"
	"database/sql"
	"sync"
)

// SchedulerLockKey is the Postgres advisory lock the scheduler leader holds.
const SchedulerLockKey int64 = 0x77667873636864 // "wfxschd"

// Leader is a lock that at most one server instance holds at a time, so that
// work every instance would otherwise do (firing schedules) happens once.
//
// On Postgres it is a session advisory lock held on one dedicated connection:
// the instance holding it stays leader until it releases it or its connection
// dies, and then the next instance to ask takes over. SQLite is one process by
// construction, so there it is always held.
type Leader struct {
	s   *Store
	key int64

	mu   sync.Mutex
	conn *sql.Conn
}

// NewLeader returns the lock for key. Nothing is taken until Lead.
func (s *Store) NewLeader(key int64) *Leader { return &Leader{s: s, key: key} }

// Lead reports whether this instance is the leader now, taking the lock if it
// is free.
func (l *Leader) Lead(ctx context.Context) bool {
	if l.s.d.isSQLite() {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		if l.conn.PingContext(ctx) == nil {
			return true
		}
		l.conn.Close() // the session (and the lock with it) is gone
		l.conn = nil
	}
	conn, err := l.s.db.Conn(ctx)
	if err != nil {
		return false
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil || !got {
		conn.Close()
		return false
	}
	l.conn = conn
	return true
}

// Release gives the lock up, so another instance can lead.
func (l *Leader) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return
	}
	_, _ = l.conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, l.key)
	l.conn.Close()
	l.conn = nil
}
