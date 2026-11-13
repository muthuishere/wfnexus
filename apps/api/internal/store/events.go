package store

import (
	"context"
	"time"
)

// Run events out (migration 000031): a project's event URL and signing secret,
// and the outbox the delivery loop drains.

// SetProjectEvents sets (or, with url "", clears) where a project's run events
// go. sealed is the signing secret sealed with the server key.
func (s *Store) SetProjectEvents(ctx context.Context, project, url string, sealed []byte) error {
	if err := s.RegisterProject(ctx, project); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE projects SET events_url=$2, events_secret_sealed=$3 WHERE name=$1`, project, url, sealed)
	return err
}

// ProjectEvents returns a project's event URL and sealed secret; url "" means
// none is configured.
func (s *Store) ProjectEvents(ctx context.Context, project string) (url string, sealed []byte, err error) {
	err = s.qrow(ctx, `SELECT events_url, events_secret_sealed FROM projects WHERE name=$1`, project).Scan(&url, &sealed)
	return url, sealed, err
}

// Delivery is one outbox row.
type Delivery struct {
	EventID  string
	Project  string
	Type     string
	Body     string
	Attempts int
	NextNs   int64
}

// EnqueueEvent writes an event to the outbox. The same id twice is a no-op.
func (s *Store) EnqueueEvent(ctx context.Context, id, project, typ, body string) error {
	now := time.Now().UnixNano()
	_, err := s.exec(ctx, `INSERT INTO event_deliveries (event_id, project, type, body, next_ns, created_ns)
		VALUES ($1,$2,$3,$4,$5,$5) ON CONFLICT (event_id) DO NOTHING`, id, project, typ, body, now)
	return err
}

// DueEvents lists undelivered events whose next attempt is due, oldest first.
func (s *Store) DueEvents(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.query(ctx, `SELECT event_id, project, type, body, attempts, next_ns FROM event_deliveries
		WHERE delivered_ns=0 AND next_ns<=$1 ORDER BY created_ns LIMIT $2`, time.Now().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.EventID, &d.Project, &d.Type, &d.Body, &d.Attempts, &d.NextNs); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClaimEvent leases a due event until `until`, so two delivery loops (two
// server instances) do not post it at the same moment. False: someone else
// has it.
func (s *Store) ClaimEvent(ctx context.Context, id string, seenNext int64, until time.Time) (bool, error) {
	res, err := s.exec(ctx, `UPDATE event_deliveries SET next_ns=$3
		WHERE event_id=$1 AND next_ns=$2 AND delivered_ns=0`, id, seenNext, until.UnixNano())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// EventDelivered records a 2xx from the receiver.
func (s *Store) EventDelivered(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `UPDATE event_deliveries SET delivered_ns=$2, attempts=attempts+1, last_error='' WHERE event_id=$1`,
		id, time.Now().UnixNano())
	return err
}

// EventFailed records a failed attempt and when to try next. A negative
// delivered marker (dead) stops retries for good.
func (s *Store) EventFailed(ctx context.Context, id, why string, next time.Time, dead bool) error {
	delivered := int64(0)
	if dead {
		delivered = -1
	}
	_, err := s.exec(ctx, `UPDATE event_deliveries SET attempts=attempts+1, last_error=$2, next_ns=$3, delivered_ns=$4
		WHERE event_id=$1`, id, why, next.UnixNano(), delivered)
	return err
}
