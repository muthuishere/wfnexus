package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

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
//  3. Messages arriving within ConversationDebounce of each other in one chat
//     are batched into ONE run: the run's input is the last message's input,
//     plus `messages`, the list of every batched message.
//
// The entry point is StartForConversation, which the router calls for every
// matched event. Lanes and batches are in memory, per process — like the
// scheduler, one engine owns the runs it starts.

// ConversationDebounce is how long a chat stays quiet before its batched
// messages start a run.
const ConversationDebounce = 3 * time.Second

// conversationDebounceMax caps how long a chat that never goes quiet can hold
// its batch back.
const conversationDebounceMax = 15 * time.Second

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
// input.message.channel + input.message.chat (the received-event contract),
// with input.subscription.channel as the channel's fallback.
func ConversationKeyFromInput(input map[string]any) (ConversationKey, bool) {
	msg, _ := input["message"].(map[string]any)
	if msg == nil {
		return ConversationKey{}, false
	}
	chat, _ := msg["chat"].(string)
	ch, _ := msg["channel"].(string)
	if ch == "" {
		if sub, _ := input["subscription"].(map[string]any); sub != nil {
			ch, _ = sub["channel"].(string)
		}
	}
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
	// debounce overrides ConversationDebounce (tests).
	debounce time.Duration
	// completed are the children whose `workflow.completed` was delivered.
	completed map[uuid.UUID]bool
}

type convLane struct {
	key     ConversationKey
	busy    bool
	run     uuid.UUID
	open    map[string]*convBatch // debouncing, by workflow
	pending []*convBatch          // ready, waiting for the lane
}

type convBatch struct {
	workflow string
	inputs   []map[string]any
	first    time.Time
	timer    *time.Timer
	// gen invalidates a superseded timer.
	gen int
}

func (c *convLanes) lane(key ConversationKey) *convLane {
	if c.lanes == nil {
		c.lanes = map[string]*convLane{}
		c.byRun = map[uuid.UUID]string{}
	}
	l := c.lanes[key.String()]
	if l == nil {
		l = &convLane{key: key, open: map[string]*convBatch{}}
		c.lanes[key.String()] = l
	}
	return l
}

func (e *Engine) debounceWindow() time.Duration {
	if e.conv.debounce > 0 {
		return e.conv.debounce
	}
	return ConversationDebounce
}

// SetConversationDebounce changes the batching window (0 restores the
// default). Tests and an operator who wants instant replies use it.
func (e *Engine) SetConversationDebounce(d time.Duration) {
	e.conv.mu.Lock()
	e.conv.debounce = d
	e.conv.mu.Unlock()
}

// StartForConversation is how the router hands a matched event to its target
// workflow (ADR 0026). It returns at once; the run starts after the chat has
// been quiet for ConversationDebounce, and only when no other run of the same
// conversation is active. Messages that arrive in the window share one run.
//
// key is the conversation — (channel, chat). input is the run input the
// contract defines: {message: <received event>, subscription: {...}}. An
// error means the request itself is unusable (no such workflow, no key); a
// failure to create the run later is logged and the lane moves on.
func (e *Engine) StartForConversation(ctx context.Context, key ConversationKey, workflowName string, input map[string]any) error {
	return e.enqueueConversation(key, workflowName, input, true)
}

// enqueueConversation queues input for the conversation's lane; debounce
// false skips the batching window (a `workflow.completed` event is not a
// message and is never merged with one).
func (e *Engine) enqueueConversation(key ConversationKey, workflowName string, input map[string]any, debounce bool) error {
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
	if !debounce {
		l.pending = append(l.pending, &convBatch{workflow: workflowName, inputs: []map[string]any{input}})
		go e.pump(key.String())
		return nil
	}
	b := l.open[workflowName]
	if b == nil {
		b = &convBatch{workflow: workflowName, first: time.Now()}
		l.open[workflowName] = b
	}
	b.inputs = append(b.inputs, input)
	if b.timer != nil {
		b.timer.Stop()
	}
	b.gen++
	gen := b.gen
	wait := e.debounceWindow()
	if left := conversationDebounceMax - time.Since(b.first); left < wait {
		wait = max(left, 0)
	}
	b.timer = time.AfterFunc(wait, func() { e.flushBatch(key, workflowName, gen) })
	return nil
}

// flushBatch moves a quiet batch to the lane's queue and starts it if the
// lane is free.
func (e *Engine) flushBatch(key ConversationKey, workflowName string, gen int) {
	e.conv.mu.Lock()
	l := e.conv.lane(key)
	b := l.open[workflowName]
	if b == nil || b.gen != gen {
		e.conv.mu.Unlock()
		return // a later message re-armed the timer
	}
	delete(l.open, workflowName)
	l.pending = append(l.pending, b)
	e.conv.mu.Unlock()
	e.pump(key.String())
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
			if l != nil && !l.busy && len(l.pending) == 0 && len(l.open) == 0 {
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

// batchInput is the run input for a batch: the LAST input, plus `messages`,
// every batched input's message in arrival order.
func batchInput(inputs []map[string]any) map[string]any {
	last := inputs[len(inputs)-1]
	out := make(map[string]any, len(last)+1)
	for k, v := range last {
		out[k] = v
	}
	var msgs []any
	for _, in := range inputs {
		if m, ok := in["message"]; ok {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) > 0 {
		out["messages"] = msgs
	}
	return out
}

// launchBatch creates (but does not start) the run for a batch.
func (e *Engine) launchBatch(key ConversationKey, b *convBatch) (uuid.UUID, error) {
	ctx := context.Background()
	input := batchInput(b.inputs)
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
