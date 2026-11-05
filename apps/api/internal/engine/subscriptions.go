package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Subscriptions and the router (ADR 0026, decisions 1 and 3).
//
// A subscription is data in registries.json; the catalog watcher reloads it
// without a restart. Every received message is offered to the router, which
// matches it to subscriptions by (channel, chat, event kind), applies the
// allow list and the group activation rule, drops backlog, dedupes on
// (subscription, message id) in the store, and starts each target workflow
// as its own run with input.message and input.subscription.

// subscriptionStore is the persistence the router needs. Like channelStore it
// is optional, so the test fakes that predate it are untouched.
type subscriptionStore interface {
	ClaimSubscriptionDelivery(ctx context.Context, d model.SubscriptionDelivery) (bool, error)
	SetSubscriptionDelivery(ctx context.Context, subscription, msgID, runID, outcome string) error
	ListSubscriptionDeliveries(ctx context.Context, subscription string, limit int) ([]*model.SubscriptionDelivery, error)
}

func (e *Engine) subStore() (subscriptionStore, bool) {
	ss, ok := e.store.(subscriptionStore)
	return ss, ok
}

// Delivery outcomes, as the router records them.
const (
	DeliveryStarted  = "started"  // a run was started
	DeliveryObserved = "observed" // observe mode: stored, no run
	DeliveryBacklog  = "backlog"  // sent before the subscription started: stored, never answered
	DeliveryRefused  = "refused"  // the sender is not on the allow list
	DeliveryInactive = "inactive" // a group message that neither mentions nor replies to us
	DeliverySkipped  = "skipped"  // an exclusive subscription took it
	DeliveryFailed   = "failed"   // the run could not be started
)

// SubscriptionInfo is a subscription as the API and CLI show it.
type SubscriptionInfo struct {
	catalog.Subscription
	EffectiveMode string `json:"effectiveMode"`
	// Problem is why the subscription cannot route right now (channel or
	// workflow missing), even when its stored status is active.
	Problem string `json:"problem,omitempty"`
}

func (e *Engine) subscriptionsFromCatalog() []catalog.Subscription {
	cat := e.Catalog()
	if cat == nil {
		return nil
	}
	return cat.SubscriptionList()
}

// Subscriptions lists every subscription with its live check.
func (e *Engine) Subscriptions() []SubscriptionInfo {
	out := []SubscriptionInfo{}
	for _, s := range e.subscriptionsFromCatalog() {
		out = append(out, e.subInfo(s))
	}
	return out
}

func (e *Engine) subInfo(s catalog.Subscription) SubscriptionInfo {
	info := SubscriptionInfo{Subscription: s, EffectiveMode: s.EffectiveMode()}
	if info.Status == "" {
		info.Status = catalog.SubValidating
	}
	if s.Status != catalog.SubError {
		if err := e.checkSubscription(s); err != nil {
			info.Problem = err.Error()
		}
	}
	return info
}

// GetSubscription is one subscription, from the live catalog.
func (e *Engine) GetSubscription(name string) (SubscriptionInfo, bool) {
	cat := e.Catalog()
	if cat == nil {
		return SubscriptionInfo{}, false
	}
	s, ok := cat.Subscriptions[name]
	if !ok {
		return SubscriptionInfo{}, false
	}
	return e.subInfo(s), true
}

// checkSubscription is the live check: the fields are well formed, the
// channel is registered, and the target workflow is loaded and may be started
// by a subscription.
func (e *Engine) checkSubscription(s catalog.Subscription) error {
	if err := catalog.ValidateSubscription(s); err != nil {
		return err
	}
	if _, err := e.channelEntry(s.Channel); err != nil {
		return err
	}
	def := e.Definitions()[s.Workflow]
	if def == nil {
		return fmt.Errorf("workflow %q is not loaded", s.Workflow)
	}
	if def.Template.Is {
		return fmt.Errorf("workflow %q is a template: copy it and subscribe the copy", s.Workflow)
	}
	if !def.On.Allows(workflow.TriggerSubscription) {
		return fmt.Errorf("workflow %q cannot be started by a subscription — it declares `on: %s` (needs workflow_dispatch or message)",
			s.Workflow, strings.Join(def.On.Names(), ", "))
	}
	return nil
}

// SaveSubscription adds or replaces a subscription. It is validated on the
// way in: it is stored active (from now) when every check passes, and with
// status error and the reason when a live check fails. A static error (a bad
// field) is refused outright. A paused subscription that is updated stays
// paused.
func (e *Engine) SaveSubscription(s catalog.Subscription, by string) (SubscriptionInfo, error) {
	if e.cfg.RegistriesPath == "" {
		return SubscriptionInfo{}, fmt.Errorf("no registries path is configured")
	}
	if err := catalog.ValidateSubscription(withStatus(s, "")); err != nil {
		return SubscriptionInfo{}, err
	}
	now := time.Now().UTC()
	prev, existed, err := catalog.ReadSubscription(e.cfg.RegistriesPath, s.Name)
	if err != nil {
		return SubscriptionInfo{}, err
	}
	if existed {
		s.CreatedAt = prev.CreatedAt
		if s.Owner == "" {
			s.Owner = prev.Owner
		}
	} else {
		s.CreatedAt = &now
	}
	if s.Owner == "" {
		s.Owner = by
	}
	s.UpdatedAt = &now
	s.Error = ""
	if existed && prev.Status == catalog.SubPaused {
		s.Status, s.StartedAt = catalog.SubPaused, prev.StartedAt
		if err := e.checkSubscription(s); err != nil {
			s.Error = err.Error()
		}
	} else {
		e.activate(&s, prev, existed, now)
	}
	if err := catalog.SaveSubscription(e.cfg.RegistriesPath, s); err != nil {
		return SubscriptionInfo{}, err
	}
	return e.reloadAndGet(s.Name)
}

func withStatus(s catalog.Subscription, st string) catalog.Subscription {
	s.Status = st
	return s
}

// activate runs the live check and sets status active (keeping the start
// time of an already-active subscription) or error.
func (e *Engine) activate(s *catalog.Subscription, prev catalog.Subscription, existed bool, now time.Time) {
	s.Status = catalog.SubValidating
	if err := e.checkSubscription(*s); err != nil {
		s.Status, s.Error = catalog.SubError, err.Error()
		return
	}
	s.Status, s.Error = catalog.SubActive, ""
	if existed && prev.Status == catalog.SubActive && prev.StartedAt != nil {
		s.StartedAt = prev.StartedAt
	} else {
		s.StartedAt = &now
	}
}

// SetSubscriptionState pauses, resumes or re-validates a subscription.
// action is pause | resume | validate. Resuming starts it from now: what
// arrived while it was paused is backlog and never answered.
func (e *Engine) SetSubscriptionState(name, action string) (SubscriptionInfo, error) {
	if e.cfg.RegistriesPath == "" {
		return SubscriptionInfo{}, fmt.Errorf("no registries path is configured")
	}
	s, ok, err := catalog.ReadSubscription(e.cfg.RegistriesPath, name)
	if err != nil {
		return SubscriptionInfo{}, err
	}
	if !ok {
		return SubscriptionInfo{}, fmt.Errorf("no subscription named %q", name)
	}
	now := time.Now().UTC()
	s.UpdatedAt = &now
	switch action {
	case "pause":
		s.Status = catalog.SubPaused
	case "resume":
		e.activate(&s, catalog.Subscription{}, false, now)
	case "validate":
		if s.Status == catalog.SubPaused {
			s.Error = ""
			if err := e.checkSubscription(withStatus(s, "")); err != nil {
				s.Error = err.Error()
			}
		} else {
			e.activate(&s, s, true, now)
		}
	default:
		return SubscriptionInfo{}, fmt.Errorf("unknown action %q (pause, resume, validate)", action)
	}
	if err := catalog.SaveSubscription(e.cfg.RegistriesPath, s); err != nil {
		return SubscriptionInfo{}, err
	}
	return e.reloadAndGet(name)
}

// DeleteSubscription removes a subscription. Its delivery history stays.
func (e *Engine) DeleteSubscription(name string) error {
	if e.cfg.RegistriesPath == "" {
		return fmt.Errorf("no registries path is configured")
	}
	if _, ok, err := catalog.ReadSubscription(e.cfg.RegistriesPath, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no subscription named %q", name)
	}
	if err := catalog.DeleteSubscription(e.cfg.RegistriesPath, name); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// SubscriptionDeliveries is what the router did, newest first.
func (e *Engine) SubscriptionDeliveries(ctx context.Context, name string, limit int) ([]*model.SubscriptionDelivery, error) {
	ss, ok := e.subStore()
	if !ok {
		return []*model.SubscriptionDelivery{}, nil
	}
	return ss.ListSubscriptionDeliveries(ctx, name, limit)
}

func (e *Engine) reloadAndGet(name string) (SubscriptionInfo, error) {
	if err := e.ReloadDefinitions(); err != nil {
		return SubscriptionInfo{}, fmt.Errorf("saved, but the catalog did not reload: %w", err)
	}
	info, ok := e.GetSubscription(name)
	if !ok {
		return SubscriptionInfo{}, fmt.Errorf("saved, but %q is not in the reloaded catalog", name)
	}
	return info, nil
}

// activeSubscriptions are the subscriptions on a channel that route now.
func (e *Engine) activeSubscriptions(channel string) []catalog.Subscription {
	var out []catalog.Subscription
	for _, s := range e.subscriptionsFromCatalog() {
		if s.Channel == channel && s.Status == catalog.SubActive {
			out = append(out, s)
		}
	}
	return out
}

// receiveEvery is how often a channel is read: its own receiveEvery (1m by
// default), or the shortest interval of its active subscriptions when that is
// shorter. One read per channel serves every subscription on it.
func (e *Engine) receiveEvery(c catalog.Channel) (time.Duration, bool) {
	subs := e.activeSubscriptions(c.Name)
	if !c.Inbound && len(subs) == 0 {
		return 0, false
	}
	every := time.Duration(0)
	if c.Inbound {
		every = defaultReceiveEach
		if d, err := time.ParseDuration(c.ReceiveEvery); err == nil && d > 0 {
			every = d
		}
	}
	for _, s := range subs {
		if d := s.IntervalDuration(); every == 0 || d < every {
			every = d
		}
	}
	return every, true
}

// messageEvent is the event map a target workflow receives as input.message.
func messageEvent(channel string, m notify.Message) map[string]any {
	ev := map[string]any{
		"channel": channel, "id": m.ID, "from": m.From, "name": m.Name, "text": m.Text,
		"at": m.At, "attachments": m.Attachments, "kind": m.EventKind(),
	}
	if m.Chat != "" {
		ev["chat"] = m.Chat
	}
	if m.ChatName != "" {
		ev["chatName"] = m.ChatName
	}
	if m.ChatType != "" {
		ev["chatType"] = m.ChatType
	}
	if m.ReplyTo != nil {
		ev["replyTo"] = m.ReplyTo
	}
	if m.Media != nil {
		ev["media"] = m.Media
	}
	if len(m.Mentions) > 0 {
		ev["mentions"] = m.Mentions
	}
	if m.FromOwner {
		ev["fromOwner"] = true
	}
	if m.Reaction != nil {
		ev["reaction"] = m.Reaction
	}
	return ev
}

// activated applies the group rule: in a group, a subscription with mention
// activation (the default) takes a message only when it mentions us or
// replies to one of ours. A DM, or activation always, always passes.
func activated(s catalog.Subscription, c catalog.Channel, m notify.Message) bool {
	if m.ChatType != "group" || s.Activation == "always" {
		return true
	}
	isSelf := func(id string) bool {
		for _, me := range c.Self {
			if me != "" && me == id {
				return true
			}
		}
		return false
	}
	for _, id := range m.Mentions {
		if isSelf(id) {
			return true
		}
	}
	return m.ReplyTo != nil && isSelf(m.ReplyTo.Sender)
}

// routeSubscriptions offers one received message to every active
// subscription. It returns the runs it started and how many subscriptions
// matched at all.
func (e *Engine) routeSubscriptions(ctx context.Context, c catalog.Channel, m notify.Message) (runs []string, matched int) {
	subs := e.activeSubscriptions(c.Name)
	if len(subs) == 0 {
		return nil, 0
	}
	ss, ok := e.subStore()
	if !ok {
		return nil, 0
	}
	kind := m.EventKind()
	exclusiveTaken := false
	// Observe and exclusive first: an exclusive subscription that takes the
	// message stops the fanout ones; observers always see it.
	ordered := make([]catalog.Subscription, 0, len(subs))
	for _, pass := range []string{catalog.ModeObserve, catalog.ModeExclusive, catalog.ModeFanout} {
		for _, s := range subs {
			if s.EffectiveMode() == pass {
				ordered = append(ordered, s)
			}
		}
	}
	for _, s := range ordered {
		if !s.Matches(c.Name, m.Chat, kind) {
			continue
		}
		if err := e.checkSubscription(s); err != nil {
			log.Printf("subscriptions: %s cannot route: %v", s.Name, err)
			continue
		}
		matched++
		outcome := ""
		switch {
		case m.At != nil && s.StartedAt != nil && m.At.Before(*s.StartedAt):
			outcome = DeliveryBacklog
		case s.EffectiveMode() == catalog.ModeObserve:
			outcome = DeliveryObserved
		case !m.FromOwner && !s.AllowsSender(m.From):
			outcome = DeliveryRefused
		case !activated(s, c, m):
			outcome = DeliveryInactive
		case exclusiveTaken:
			outcome = DeliverySkipped
		}
		fresh, err := ss.ClaimSubscriptionDelivery(ctx, model.SubscriptionDelivery{
			Subscription: s.Name, MsgID: m.ID, Channel: c.Name, Chat: m.Chat, Outcome: outcome,
		})
		if err != nil {
			log.Printf("subscriptions: %s: record delivery: %v", s.Name, err)
			continue
		}
		if !fresh {
			// Seen before: whatever it did then stands, including taking an
			// exclusive message.
			if s.EffectiveMode() == catalog.ModeExclusive {
				exclusiveTaken = true
			}
			continue
		}
		if outcome != "" {
			continue
		}
		runID, err := e.startSubscriptionRun(ctx, s, c, m)
		if err != nil {
			log.Printf("subscriptions: %s: start %s: %v", s.Name, s.Workflow, err)
			_ = ss.SetSubscriptionDelivery(ctx, s.Name, m.ID, "", DeliveryFailed)
			continue
		}
		_ = ss.SetSubscriptionDelivery(ctx, s.Name, m.ID, runID, DeliveryStarted)
		if runID != "" {
			runs = append(runs, runID)
		}
		if s.EffectiveMode() == catalog.ModeExclusive {
			exclusiveTaken = true
		}
	}
	return runs, matched
}

// subscriptionInput is the `input.subscription` the contract defines.
func subscriptionInput(s catalog.Subscription) map[string]any {
	sub := map[string]any{
		"name": s.Name, "instructions": s.Instructions, "responsePolicy": s.ResponsePolicy,
		"mode": s.EffectiveMode(), "memory": s.Memory, "skills": s.Skills, "owner": s.Owner,
	}
	if s.ResponsePolicy == "" {
		sub["responsePolicy"] = "draft"
	}
	return sub
}

// startSubscriptionRun hands a matched event to its target workflow with the
// ADR 0026 input shape. An event that names its chat goes through the
// conversation lane (one run per chat at a time, 3 s debounce), so the run
// starts later and the returned id is empty. Only a chat-less event (an
// adapter that predates the event fields) starts a run at once.
func (e *Engine) startSubscriptionRun(ctx context.Context, s catalog.Subscription, c catalog.Channel, m notify.Message) (string, error) {
	input := map[string]any{"message": messageEvent(c.Name, m), "subscription": subscriptionInput(s)}
	if m.Chat != "" {
		return "", e.StartForConversation(ctx, ConversationKey{Channel: c.Name, Chat: m.Chat}, s.Workflow, input)
	}
	raw, err := e.PrepareRun(s.Workflow, workflow.TriggerSubscription, mustJSON(input))
	if err != nil {
		return "", err
	}
	run, err := e.CreateRunAdmitted(ctx, s.Workflow, raw, false)
	if err != nil {
		return "", err
	}
	if err := e.store.SetTriggeredBy(ctx, run.ID, m.From); err != nil {
		return "", err
	}
	e.Start(run.ID)
	return run.ID.String(), nil
}

// routeCompletedEvent is the default event sink: a `workflow.completed` event
// from a run in a conversation is offered to the active subscriptions of that
// (channel, chat) that list `workflow.completed` in their events. Each one is
// claimed once per (subscription, child run) - the same dedupe as messages -
// and queued on the conversation's lane without the debounce. It reports
// whether any subscription took the event.
func (e *Engine) routeCompletedEvent(ctx context.Context, ev map[string]any) bool {
	channel, _ := ev["channel"].(string)
	chat, _ := ev["chat"].(string)
	runID, _ := ev["runId"].(string)
	if channel == "" || chat == "" || runID == "" {
		return false
	}
	ss, ok := e.subStore()
	if !ok {
		return false
	}
	handled := false
	for _, s := range e.activeSubscriptions(channel) {
		if s.EffectiveMode() == catalog.ModeObserve || !s.Matches(channel, chat, "workflow.completed") {
			continue
		}
		if err := e.checkSubscription(s); err != nil {
			continue
		}
		msgID := "workflow.completed:" + runID
		fresh, err := ss.ClaimSubscriptionDelivery(ctx, model.SubscriptionDelivery{
			Subscription: s.Name, MsgID: msgID, Channel: channel, Chat: chat,
		})
		if err != nil {
			log.Printf("subscriptions: %s: record workflow.completed: %v", s.Name, err)
			continue
		}
		if !fresh {
			handled = true // already queued once
			continue
		}
		input := map[string]any{"event": ev, "subscription": subscriptionInput(s),
			"conversation": map[string]any{"channel": channel, "chat": chat}}
		if err := e.enqueueConversation(ConversationKey{Channel: channel, Chat: chat}, s.Workflow, input, false); err != nil {
			log.Printf("subscriptions: %s: workflow.completed: %v", s.Name, err)
			_ = ss.SetSubscriptionDelivery(ctx, s.Name, msgID, "", DeliveryFailed)
			continue
		}
		_ = ss.SetSubscriptionDelivery(ctx, s.Name, msgID, "", DeliveryStarted)
		handled = true
	}
	return handled
}

// eventJSON is the ADR 0026 part of a message, as stored on its row.
func eventJSON(m notify.Message) json.RawMessage {
	ev := notify.Message{Chat: m.Chat, ChatName: m.ChatName, ChatType: m.ChatType, Kind: m.Kind,
		ReplyTo: m.ReplyTo, Media: m.Media, Mentions: m.Mentions, FromOwner: m.FromOwner, Reaction: m.Reaction}
	b, err := json.Marshal(ev)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// withEvent restores a stored row's ADR 0026 fields onto a message.
func withEvent(m notify.Message, raw json.RawMessage) notify.Message {
	if len(raw) == 0 {
		return m
	}
	var ev notify.Message
	if json.Unmarshal(raw, &ev) != nil {
		return m
	}
	m.Chat, m.ChatName, m.ChatType, m.Kind = ev.Chat, ev.ChatName, ev.ChatType, ev.Kind
	m.ReplyTo, m.Media, m.Mentions, m.FromOwner, m.Reaction = ev.ReplyTo, ev.Media, ev.Mentions, ev.FromOwner, ev.Reaction
	return m
}
