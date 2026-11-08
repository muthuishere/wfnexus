package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// CONVERSATIONS (ADR 0026 decision 4).
//
// A run whose input carries a message with a channel and a chat belongs to a
// conversation, keyed (channel, chat). Three things follow from that:
//
//  1. Memory is per conversation: the run records and digest a step reads are
//     that chat's; lessons stay workflow-wide (memory.go). `memory: {scope:
//     workflow}` opts out.
//  2. Runs of one conversation go ONE AT A TIME, in arrival order; different
//     conversations run in parallel. The lane is held while a run is queued,
//     running or waiting on a child, and released when it ends or parks on a
//     person (a pause must not block the chat forever).
//
// Batching a chat's messages is the whatsapp-group-monitor's job (ADR 0028);
// the lane here carries `workflow.completed` events back into the parent's
// conversation (start.go). Lanes are in memory, per process — like the
// scheduler, one engine owns the runs it starts.

// ConversationKey names a conversation: the channel (account) and the chat.
type ConversationKey struct {
	Channel string `json:"channel"`
	Chat    string `json:"chat"`
}

// String is the stored form, `<channel>:<chat>`. A channel name has no colon;
// a chat id may (it is split on the FIRST colon).
func (k ConversationKey) String() string {
	if k.Channel == "" || k.Chat == "" {
		return ""
	}
	return k.Channel + ":" + k.Chat
}

// ParseConversationKey reads the stored form back.
func ParseConversationKey(s string) (ConversationKey, bool) {
	ch, chat, ok := strings.Cut(s, ":")
	if !ok || ch == "" || chat == "" {
		return ConversationKey{}, false
	}
	return ConversationKey{Channel: ch, Chat: chat}, true
}

// ConversationKeyFromInput finds the conversation a run input belongs to:
// input.message.channel + input.message.chat (the received-event contract).
func ConversationKeyFromInput(input map[string]any) (ConversationKey, bool) {
	msg, _ := input["message"].(map[string]any)
	if msg == nil {
		return ConversationKey{}, false
	}
	chat, _ := msg["chat"].(string)
	ch, _ := msg["channel"].(string)
	k := ConversationKey{Channel: strings.TrimSpace(ch), Chat: strings.TrimSpace(chat)}
	return k, k.String() != ""
}

// conversationOfRun is the run's conversation key: the one recorded on the
// run, else the one its input names. "" for a run that is not from a chat.
func conversationOfRun(run *model.Run) string {
	if run == nil {
		return ""
	}
	if run.Conversation != "" {
		return run.Conversation
	}
	var input map[string]any
	_ = json.Unmarshal(run.Input, &input)
	if k, ok := ConversationKeyFromInput(input); ok {
		return k.String()
	}
	return ""
}

func (e *Engine) conversationOfRunID(ctx context.Context, runID uuid.UUID) string {
	if e.store == nil {
		return ""
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	return conversationOfRun(run)
}

// ---- lanes ----

type convLanes struct {
	mu    sync.Mutex
	lanes map[string]*convLane
	// byRun maps a lane-held run to its conversation, so a status change finds
	// its lane without reading the run.
	byRun map[uuid.UUID]string
	// completed are the children whose `workflow.completed` was delivered.
	completed map[uuid.UUID]bool
}

type convLane struct {
	key     ConversationKey
	busy    bool
	run     uuid.UUID
	pending []*convBatch // waiting for the lane
}

type convBatch struct {
	workflow string
	input    map[string]any
}

func (c *convLanes) lane(key ConversationKey) *convLane {
	if c.lanes == nil {
		c.lanes = map[string]*convLane{}
		c.byRun = map[uuid.UUID]string{}
	}
	l := c.lanes[key.String()]
	if l == nil {
		l = &convLane{key: key}
		c.lanes[key.String()] = l
	}
	return l
}

// enqueueConversation queues input for the conversation's lane: it starts
// when no other run of that conversation is active.
func (e *Engine) enqueueConversation(key ConversationKey, workflowName string, input map[string]any) error {
	if key.String() == "" {
		return fmt.Errorf("conversation: needs a channel and a chat")
	}
	if e.Definitions()[workflowName] == nil {
		return fmt.Errorf("conversation: workflow %q is not loaded", workflowName)
	}
	if input == nil {
		input = map[string]any{}
	}
	e.conv.mu.Lock()
	defer e.conv.mu.Unlock()
	l := e.conv.lane(key)
	l.pending = append(l.pending, &convBatch{workflow: workflowName, input: input})
	go e.pump(key.String())
	return nil
}

// pump starts the lane's next batch when the lane is free. The lane is
// reserved under the lock and the run is created outside it, so creating a run
// (which may cancel another, whose status change comes back here) never waits
// on this lock.
func (e *Engine) pump(keyStr string) {
	for {
		e.conv.mu.Lock()
		l := e.conv.lanes[keyStr]
		if l == nil || l.busy || len(l.pending) == 0 {
			if l != nil && !l.busy && len(l.pending) == 0 {
				delete(e.conv.lanes, keyStr)
			}
			e.conv.mu.Unlock()
			return
		}
		b := l.pending[0]
		l.pending = l.pending[1:]
		l.busy, l.run = true, uuid.Nil
		e.conv.mu.Unlock()

		id, err := e.launchBatch(l.key, b)

		e.conv.mu.Lock()
		if err != nil {
			l.busy = false
			e.conv.mu.Unlock()
			log.Printf("conversation %s: starting %s: %v", l.key, b.workflow, err)
			continue
		}
		l.run = id
		e.conv.byRun[id] = keyStr
		e.conv.mu.Unlock()
		e.Start(id)
		return
	}
}

// launchBatch creates (but does not start) the run for a batch.
func (e *Engine) launchBatch(key ConversationKey, b *convBatch) (uuid.UUID, error) {
	ctx := context.Background()
	input := b.input
	def := e.Definitions()[b.workflow]
	if def == nil {
		return uuid.Nil, fmt.Errorf("workflow %q is not loaded", b.workflow)
	}
	// A conversation's handler is started by a message; one that declares no
	// `message` trigger is still startable the way a person would start it.
	trig := workflow.TriggerMessage
	if !def.On.Allows(trig) {
		trig = workflow.TriggerDispatch
	}
	raw, err := e.PrepareRun(b.workflow, trig, mustJSON(input))
	if err != nil {
		return uuid.Nil, err
	}
	run, err := e.CreateRunAdmitted(ctx, b.workflow, raw, false)
	if err != nil {
		return uuid.Nil, err
	}
	if err := e.store.SetRunLinks(ctx, run.ID, "", "", "", key.String()); err != nil {
		return uuid.Nil, err
	}
	by := "trigger:" + string(workflow.TriggerMessage)
	if msg, _ := input["message"].(map[string]any); msg != nil {
		if from, _ := msg["from"].(string); from != "" {
			by = from
		}
	}
	if err := e.store.SetTriggeredBy(ctx, run.ID, by); err != nil {
		return uuid.Nil, err
	}
	if err := e.store.TouchConversation(ctx, model.Conversation{
		Channel: key.Channel, Chat: key.Chat, Workflow: b.workflow,
		Project: run.Project, LastRunID: run.ID.String(),
	}); err != nil {
		log.Printf("conversation %s: %v", key, err)
	}
	return run.ID, nil
}

// laneReleasing are the statuses that free a conversation's lane: the run
// ended, or it parked on a person.
var laneReleasing = map[string]bool{
	"done": true, "failed": true, "cancelled": true,
	"awaiting_approval": true, "needs_input": true,
}

// conversationAfterStatus releases a lane and moves the conversation's state.
func (e *Engine) conversationAfterStatus(ctx context.Context, runID uuid.UUID, status string) {
	if !laneReleasing[status] {
		return
	}
	e.conv.mu.Lock()
	keyStr, held := e.conv.byRun[runID]
	var next bool
	if held {
		delete(e.conv.byRun, runID)
		if l := e.conv.lanes[keyStr]; l != nil && l.run == runID {
			l.busy, l.run = false, uuid.Nil
			next = len(l.pending) > 0
		}
	}
	e.conv.mu.Unlock()
	if held {
		e.pump(keyStr)
	}

	// The state row, for any run of a conversation — lane-held or not.
	if keyStr == "" {
		keyStr = e.conversationOfRunID(ctx, runID)
	}
	key, ok := ParseConversationKey(keyStr)
	if !ok || next {
		return // a next run already reopened it as active
	}
	state := "resolved"
	if status == "awaiting_approval" || status == "needs_input" {
		state = "waiting"
	}
	c, err := e.store.GetConversation(ctx, key.Channel, key.Chat)
	if err != nil || c.State == "closed" || c.LastRunID != runID.String() {
		return // a closed one stays closed; an older run does not speak for it
	}
	_ = e.store.SetConversationState(ctx, key.Channel, key.Chat, state)
}

// Conversations lists conversations, most recent first.
func (e *Engine) Conversations(ctx context.Context, limit int) ([]model.Conversation, error) {
	return e.store.ListConversations(ctx, limit)
}

// SetConversationState moves a conversation to active, waiting, resolved or
// closed. A new message reopens a resolved or closed one.
func (e *Engine) SetConversationState(ctx context.Context, key ConversationKey, state string) error {
	switch state {
	case "active", "waiting", "resolved", "closed":
	default:
		return fmt.Errorf("conversation state: want active, waiting, resolved or closed, got %q", state)
	}
	return e.store.SetConversationState(ctx, key.Channel, key.Chat, state)
}
