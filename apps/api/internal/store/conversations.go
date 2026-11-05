package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// Conversations (ADR 0026 decision 4): one row per (channel, chat), carrying
// the state a chat is in — active | waiting | resolved | closed.

const convCols = `channel, chat, workflow, project, state, last_run_id, created_ns, updated_ns`

func scanConversation(row rowScanner) (model.Conversation, error) {
	var c model.Conversation
	var created, updated int64
	err := row.Scan(&c.Channel, &c.Chat, &c.Workflow, &c.Project, &c.State, &c.LastRunID, &created, &updated)
	c.CreatedAt = time.Unix(0, created).UTC()
	c.UpdatedAt = time.Unix(0, updated).UTC()
	return c, err
}

// TouchConversation records that a run of the conversation started: the row
// is created when missing, and a resolved or closed conversation is reopened
// (state back to active) — a new message always reopens it.
func (s *Store) TouchConversation(ctx context.Context, c model.Conversation) error {
	ns := time.Now().UnixNano()
	_, err := s.exec(ctx, `
		INSERT INTO conversations (channel, chat, workflow, project, state, last_run_id, created_ns, updated_ns)
		VALUES ($1,$2,$3,$4,'active',$5,$6,$6)
		ON CONFLICT (channel, chat) DO UPDATE SET
		    workflow=excluded.workflow, project=excluded.project, state='active',
		    last_run_id=excluded.last_run_id, updated_ns=excluded.updated_ns`,
		c.Channel, c.Chat, c.Workflow, c.Project, c.LastRunID, ns)
	return err
}

// SetConversationState moves a conversation to state. ErrNotFound when there
// is no such conversation.
func (s *Store) SetConversationState(ctx context.Context, channel, chat, state string) error {
	res, err := s.exec(ctx, `UPDATE conversations SET state=$3, updated_ns=$4 WHERE channel=$1 AND chat=$2`,
		channel, chat, state, time.Now().UnixNano())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetConversation returns one conversation, or ErrNotFound.
func (s *Store) GetConversation(ctx context.Context, channel, chat string) (model.Conversation, error) {
	c, err := scanConversation(s.qrow(ctx, `SELECT `+convCols+` FROM conversations WHERE channel=$1 AND chat=$2`, channel, chat))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListConversations returns conversations, most recently updated first.
func (s *Store) ListConversations(ctx context.Context, limit int) ([]model.Conversation, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(ctx, `SELECT `+convCols+` FROM conversations ORDER BY updated_ns DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
