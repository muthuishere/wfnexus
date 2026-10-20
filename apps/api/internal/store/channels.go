package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Channels (ADR 0023): received messages, send dedup and pause codes. The
// receive cursor lives in `settings`, under ChannelCursorKey.
//
// Every timestamp these tables COMPARE is written by Go through ts(), never by
// the database's own clock, so a window or an expiry means the same thing on
// both dialects: SQLite keeps timestamps as text, and a comparison is only
// correct when both sides were written in one format.

// ChannelCursorKey is the settings key a channel's receive cursor is kept under.
func ChannelCursorKey(channel string) string { return "channel.cursor." + channel }

// sqliteTime is the one text form SQLite timestamps are written in here; it
// sorts lexically in time order, which is what makes `<` on it correct.
const sqliteTime = "2006-01-02 15:04:05.000"

// ts is a time as this dialect stores it.
func (s *Store) ts(t time.Time) any {
	if s.d.isSQLite() {
		return t.UTC().Format(sqliteTime)
	}
	return t.UTC()
}

// InsertChannelMessage stores a received message ONCE, as not yet handled. It
// reports false when (channel, msg_id) was already there — a re-delivery,
// which the caller must not act on a second time.
func (s *Store) InsertChannelMessage(ctx context.Context, m *ChannelMessage) (bool, error) {
	att := m.Attachments
	if len(att) == 0 {
		att = []byte("[]")
	}
	res, err := s.exec(ctx, `
		INSERT INTO channel_messages (channel, msg_id, sender, sender_name, text, attachments, sent_at, received_at, handled)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8, false)
		ON CONFLICT (channel, msg_id) DO NOTHING`,
		m.Channel, m.MsgID, m.Sender, m.SenderName, m.Text, string(att), m.SentAt, s.ts(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetChannelMessageOutcome records what a received message did, and marks it
// handled.
func (s *Store) SetChannelMessageOutcome(ctx context.Context, channel, msgID, runID, outcome string) error {
	_, err := s.exec(ctx, `UPDATE channel_messages SET run_id=$3, outcome=$4, handled=true WHERE channel=$1 AND msg_id=$2`,
		channel, msgID, runID, outcome)
	return err
}

const channelMessageCols = `cm.id, cm.channel, cm.msg_id, cm.sender, cm.sender_name, cm.text, cm.attachments, cm.sent_at, cm.run_id, cm.outcome, cm.received_at, cm.handled`

func scanChannelMessages(rows *sql.Rows) ([]*ChannelMessage, error) {
	defer rows.Close()
	out := []*ChannelMessage{}
	for rows.Next() {
		var m ChannelMessage
		if err := rows.Scan(&m.ID, &m.Channel, &m.MsgID, &m.Sender, &m.SenderName, &m.Text,
			rawJSON{&m.Attachments}, &m.SentAt, &m.RunID, &m.Outcome, &m.ReceivedAt, &m.Handled); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ListChannelMessages is newest first.
func (s *Store) ListChannelMessages(ctx context.Context, f ChannelMessageFilter) ([]*ChannelMessage, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	mode := "linked"
	if f.All && f.Project == "" {
		mode = "all"
	}
	// workflow_runs.id is a uuid in Postgres and text in SQLite; ::text is
	// dropped by the SQLite dialect, so one spelling serves both.
	rows, err := s.query(ctx, `
		SELECT `+channelMessageCols+` FROM channel_messages cm
		WHERE ($1 = '' OR cm.channel=$1)
		  AND ($2 = '' OR cm.run_id=$2 OR EXISTS (
		        SELECT 1 FROM channel_message_runs mr
		        WHERE mr.channel=cm.channel AND mr.msg_id=cm.msg_id AND mr.run_id=$2))
		  AND ($3 = 'all'
		    OR EXISTS (SELECT 1 FROM workflow_runs wr
		               WHERE wr.id::text = cm.run_id AND ($4 = '' OR wr.project=$4))
		    OR EXISTS (SELECT 1 FROM channel_message_runs mr JOIN workflow_runs wr ON wr.id::text = mr.run_id
		               WHERE mr.channel=cm.channel AND mr.msg_id=cm.msg_id AND ($4 = '' OR wr.project=$4)))
		ORDER BY cm.id DESC LIMIT $5`, f.Channel, f.RunID, mode, f.Project, f.Limit)
	if err != nil {
		return nil, err
	}
	return scanChannelMessages(rows)
}

// PendingChannelMessages are stored but not yet acted on, oldest first.
func (s *Store) PendingChannelMessages(ctx context.Context, channel string) ([]*ChannelMessage, error) {
	rows, err := s.query(ctx, `
		SELECT `+channelMessageCols+` FROM channel_messages cm
		WHERE cm.channel=$1 AND cm.handled=false ORDER BY cm.id LIMIT 500`, channel)
	if err != nil {
		return nil, err
	}
	return scanChannelMessages(rows)
}

// DedupClaim decides, in ONE statement, whether a send under (channel, key)
// may go out now: it may when the key was never used or was last used more
// than `window` ago (window <= 0 means once, ever). Two runs claiming at the
// same moment cannot both win — the upsert's WHERE is evaluated against the
// row as it stands under the conflict lock. RETURNING needs SQLite 3.35+.
func (s *Store) DedupClaim(ctx context.Context, channel, key string, window time.Duration) (bool, time.Time, error) {
	now := time.Now().UTC()
	cutoff := time.Time{} // window <= 0: nothing is ever old enough
	if window > 0 {
		cutoff = now.Add(-window)
	}
	var got time.Time
	err := s.qrow(ctx, `
		INSERT INTO channel_dedup (channel, dedup_key, sent_at) VALUES ($1,$2,$3)
		ON CONFLICT (channel, dedup_key) DO UPDATE SET sent_at=EXCLUDED.sent_at
		WHERE channel_dedup.sent_at < $4
		RETURNING sent_at`, channel, key, s.ts(now), s.ts(cutoff)).Scan(&got)
	if err == nil {
		return true, time.Time{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, err
	}
	// Not claimed. The previous send's time is only for the log line.
	var last time.Time
	_ = s.qrow(ctx, `SELECT sent_at FROM channel_dedup WHERE channel=$1 AND dedup_key=$2`, channel, key).Scan(&last)
	return false, last, nil
}

// DedupRelease forgets a claim whose send then DEFINITELY failed, so the next
// attempt is not swallowed as a duplicate of a message that never went out.
func (s *Store) DedupRelease(ctx context.Context, channel, key string) error {
	_, err := s.exec(ctx, `DELETE FROM channel_dedup WHERE channel=$1 AND dedup_key=$2`, channel, key)
	return err
}

// InsertPauseCode records a code; false when (channel, code) is taken, and the
// existing row is never touched — the caller draws another code.
func (s *Store) InsertPauseCode(ctx context.Context, c PauseCode) (bool, error) {
	res, err := s.exec(ctx, `
		INSERT INTO pause_codes (channel, code, run_id, step_id, kind, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (channel, code) DO NOTHING`,
		c.Channel, c.Code, c.RunID, c.StepID, c.Kind, s.ts(time.Now()), s.ts(c.ExpiresAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RetirePauseCodes retires every live code for a step: its pause resolved, or
// it paused again and a new code supersedes the old.
func (s *Store) RetirePauseCodes(ctx context.Context, runID, stepID string) error {
	_, err := s.exec(ctx, `UPDATE pause_codes SET retired_at=$3 WHERE run_id=$1 AND step_id=$2 AND retired_at IS NULL`,
		runID, stepID, s.ts(time.Now()))
	return err
}

// PauseByCode looks a code up ON A CHANNEL; nil when nobody issued it there.
// Whether it is still live is the caller's check (PauseCode.Live).
func (s *Store) PauseByCode(ctx context.Context, channel, code string) (*PauseCode, error) {
	var c PauseCode
	err := s.qrow(ctx, `
		SELECT channel, code, run_id, step_id, kind, created_at, expires_at, retired_at
		FROM pause_codes WHERE channel=$1 AND code=$2`, channel, code).
		Scan(&c.Channel, &c.Code, &c.RunID, &c.StepID, &c.Kind, &c.CreatedAt, &c.ExpiresAt, &c.RetiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// PruneChannels is retention: handled messages received before msgsBefore,
// and pause codes that expired or were retired before codesBefore.
func (s *Store) PruneChannels(ctx context.Context, msgsBefore, codesBefore time.Time) error {
	if _, err := s.exec(ctx, `DELETE FROM channel_messages WHERE handled=true AND received_at < $1`, s.ts(msgsBefore)); err != nil {
		return err
	}
	// A claim outlives nothing it guards: it goes with the messages' window.
	if _, err := s.exec(ctx, `DELETE FROM channel_message_runs WHERE created_at < $1`, s.ts(msgsBefore)); err != nil {
		return err
	}
	_, err := s.exec(ctx, `DELETE FROM pause_codes WHERE expires_at < $1 OR retired_at < $1`, s.ts(codesBefore))
	return err
}

// ClaimMessageRun takes the right to start `workflow` for one received
// message. Only one caller ever wins, so a message that is handled twice (a
// crash before its outcome was recorded) starts the workflow once.
func (s *Store) ClaimMessageRun(ctx context.Context, channel, msgID, workflow string) (bool, error) {
	res, err := s.exec(ctx, `
		INSERT INTO channel_message_runs (channel, msg_id, workflow, created_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (channel, msg_id, workflow) DO NOTHING`, channel, msgID, workflow, s.ts(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetMessageRun records the run a claim started.
func (s *Store) SetMessageRun(ctx context.Context, channel, msgID, workflow, runID string) error {
	_, err := s.exec(ctx, `UPDATE channel_message_runs SET run_id=$4 WHERE channel=$1 AND msg_id=$2 AND workflow=$3`,
		channel, msgID, workflow, runID)
	return err
}

// ReleaseMessageRun gives a claim back when the run could not be created at
// all (the workflow refused the input, say), so nothing claims a run that
// does not exist.
func (s *Store) ReleaseMessageRun(ctx context.Context, channel, msgID, workflow string) error {
	_, err := s.exec(ctx, `DELETE FROM channel_message_runs WHERE channel=$1 AND msg_id=$2 AND workflow=$3`,
		channel, msgID, workflow)
	return err
}
