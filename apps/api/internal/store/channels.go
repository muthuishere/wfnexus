package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Channels (ADR 0023): received messages, send dedup and pause codes. The
// receive cursor lives in `settings`, under ChannelCursorKey.

// ChannelCursorKey is the settings key a channel's receive cursor is kept under.
func ChannelCursorKey(channel string) string { return "channel.cursor." + channel }

// InsertChannelMessage stores a received message ONCE. It reports false when
// (channel, msg_id) was already there — a re-delivery, which the caller must
// not act on a second time.
func (s *Store) InsertChannelMessage(ctx context.Context, m *ChannelMessage) (bool, error) {
	att := m.Attachments
	if len(att) == 0 {
		att = []byte("[]")
	}
	res, err := s.exec(ctx, `
		INSERT INTO channel_messages (channel, msg_id, sender, sender_name, text, attachments, sent_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7, now())
		ON CONFLICT (channel, msg_id) DO NOTHING`,
		m.Channel, m.MsgID, m.Sender, m.SenderName, m.Text, string(att), m.SentAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetChannelMessageOutcome records what a received message did.
func (s *Store) SetChannelMessageOutcome(ctx context.Context, channel, msgID, runID, outcome string) error {
	_, err := s.exec(ctx, `UPDATE channel_messages SET run_id=$3, outcome=$4 WHERE channel=$1 AND msg_id=$2`,
		channel, msgID, runID, outcome)
	return err
}

// ListChannelMessages is newest first, narrowed by channel and/or run.
func (s *Store) ListChannelMessages(ctx context.Context, channel, runID string, limit int) ([]*ChannelMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.query(ctx, `
		SELECT id, channel, msg_id, sender, sender_name, text, attachments, sent_at, run_id, outcome, received_at
		FROM channel_messages
		WHERE ($1 = '' OR channel=$1) AND ($2 = '' OR run_id=$2)
		ORDER BY id DESC LIMIT $3`, channel, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ChannelMessage{}
	for rows.Next() {
		var m ChannelMessage
		if err := rows.Scan(&m.ID, &m.Channel, &m.MsgID, &m.Sender, &m.SenderName, &m.Text,
			rawJSON{&m.Attachments}, &m.SentAt, &m.RunID, &m.Outcome, &m.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// DedupClaim decides whether a send under (channel, key) may go out now: it
// may when the key was never used or was last used more than `window` ago, and
// a send that may go out is recorded in the same call. The comparison is done
// here rather than in SQL so both dialects read the clock the same way.
func (s *Store) DedupClaim(ctx context.Context, channel, key string, window time.Duration) (bool, time.Time, error) {
	var last time.Time
	err := s.qrow(ctx, `SELECT sent_at FROM channel_dedup WHERE channel=$1 AND dedup_key=$2`, channel, key).Scan(&last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, time.Time{}, err
	default:
		if window <= 0 || time.Since(last.UTC()) < window {
			return false, last, nil
		}
	}
	_, err = s.exec(ctx, `
		INSERT INTO channel_dedup (channel, dedup_key, sent_at) VALUES ($1,$2, now())
		ON CONFLICT (channel, dedup_key) DO UPDATE SET sent_at=now()`, channel, key)
	return err == nil, last, err
}

// DedupRelease forgets a claim whose send then failed, so the next attempt is
// not swallowed as a duplicate of a message that never went out.
func (s *Store) DedupRelease(ctx context.Context, channel, key string) error {
	_, err := s.exec(ctx, `DELETE FROM channel_dedup WHERE channel=$1 AND dedup_key=$2`, channel, key)
	return err
}

// PutPauseCode records the code an outbound pause carries.
func (s *Store) PutPauseCode(ctx context.Context, c PauseCode) error {
	_, err := s.exec(ctx, `
		INSERT INTO pause_codes (code, run_id, step_id, kind) VALUES ($1,$2,$3,$4)
		ON CONFLICT (code) DO UPDATE SET run_id=EXCLUDED.run_id, step_id=EXCLUDED.step_id, kind=EXCLUDED.kind`,
		c.Code, c.RunID, c.StepID, c.Kind)
	return err
}

// PauseByCode looks a code up; nil when nobody issued it.
func (s *Store) PauseByCode(ctx context.Context, code string) (*PauseCode, error) {
	var c PauseCode
	err := s.qrow(ctx, `SELECT code, run_id, step_id, kind FROM pause_codes WHERE code=$1`, code).
		Scan(&c.Code, &c.RunID, &c.StepID, &c.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}
