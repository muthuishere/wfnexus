package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Run events out (migration 000029): a project's event URL and signing secret,
// and the outbox the delivery loop drains.

// SetProjectEvents sets where a project's run events go; url "" clears it.
// sealed is the signing secret sealed with the server key.
func (s *Store) SetProjectEvents(ctx context.Context, project, url string, sealed []byte) error {
	if url == "" {
		_, err := s.exec(ctx, `DELETE FROM project_events WHERE project=$1`, project)
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO project_events (project, url, secret_sealed) VALUES ($1,$2,$3)
		ON CONFLICT (project) DO UPDATE SET url=$2, secret_sealed=$3`, project, url, sealed)
	return err
}

// ProjectEvents returns a project's event URL and sealed secret; url "" means
// none is configured.
func (s *Store) ProjectEvents(ctx context.Context, project string) (url string, sealed []byte, err error) {
	err = s.qrow(ctx, `SELECT url, secret_sealed FROM project_events WHERE project=$1`, project).Scan(&url, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
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

// EnqueueEvent writes the next occurrence of a run transition to the outbox
// and returns its id, `<stem>.<seq>`. body is built from that id.
func (s *Store) EnqueueEvent(ctx context.Context, stem, runID, project, typ string, body func(id string) string) (string, error) {
	var seq int
	if err := s.qrow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM event_deliveries WHERE stem=$1`, stem).Scan(&seq); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s.%d", stem, seq)
	now := time.Now().UnixNano()
	_, err := s.exec(ctx, `INSERT INTO event_deliveries (event_id, stem, seq, run_id, project, type, body, next_ns, created_ns)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, id, stem, seq, runID, project, typ, body(id), now)
	return id, err
}

// LastRunEventType is the type of the newest event queued for a run, or "".
func (s *Store) LastRunEventType(ctx context.Context, runID string) (string, error) {
	var t string
	err := s.qrow(ctx, `SELECT type FROM event_deliveries WHERE run_id=$1 ORDER BY created_ns DESC LIMIT 1`, runID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return t, err
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

// EventFailed records a failed attempt and when to try next. dead stops
// retries for good (delivered_ns = -1).
func (s *Store) EventFailed(ctx context.Context, id, why string, next time.Time, dead bool) error {
	delivered := int64(0)
	if dead {
		delivered = -1
	}
	_, err := s.exec(ctx, `UPDATE event_deliveries SET attempts=attempts+1, last_error=$2, next_ns=$3, delivered_ns=$4
		WHERE event_id=$1`, id, why, next.UnixNano(), delivered)
	return err
}
