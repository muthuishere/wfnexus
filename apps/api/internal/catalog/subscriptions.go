package catalog

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Subscription (ADR 0026) says which messages on a channel start which
// workflow. It is data in registries.json under `subscriptions`, so a change
// is picked up by the catalog watcher without a restart.
//
// The fields a person sets are the routing (channel, chat, events, mode,
// activation, allow), the target (workflow, version) and what the target is
// told (skills, instructions, responsePolicy, memory). Status and StartedAt
// are written by the server: a subscription moves validating → active ⇄
// paused, and a failed check moves it to error.
type Subscription struct {
	// Name is the map KEY on disk.
	Name    string `json:"name,omitempty"`
	Channel string `json:"channel"`
	// Chat is the conversation id (a WhatsApp JID). "*" matches every chat on
	// the channel, which is only sensible for an observe subscription.
	Chat string `json:"chat"`
	// Events are the event kinds that match: message, reaction, updated,
	// deleted. Empty means ["message"].
	Events []string `json:"events,omitempty"`
	// Interval is how often the channel is read for this subscription:
	// 30s, 1m (default) or 2m.
	Interval       string   `json:"interval,omitempty"`
	Workflow       string   `json:"workflow"`
	Version        string   `json:"version,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	Instructions   string   `json:"instructions,omitempty"`
	ResponsePolicy string   `json:"responsePolicy,omitempty"` // draft | approval | auto | none
	Memory         string   `json:"memory,omitempty"`
	// Mode is fanout, exclusive or observe. Empty: exclusive when the
	// subscription replies (responsePolicy is not none), else fanout.
	Mode string `json:"mode,omitempty"`
	// Activation is mention or always. In a group, mention (the default)
	// starts a run only when we are mentioned or a message replies to ours.
	Activation string `json:"activation,omitempty"`
	// Allow is the explicit list of sender ids that may start a run. Empty
	// means nobody; "*" is an explicit everyone.
	Allow []string `json:"allow,omitempty"`
	Owner string   `json:"owner,omitempty"`
	// Status: validating | active | paused | error | deleted.
	Status string `json:"status,omitempty"`
	// Error is why the last validation failed (status error).
	Error string `json:"error,omitempty"`
	// StartedAt is when the subscription last became active. Anything sent
	// before it is backlog: stored, never answered.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Subscription statuses.
const (
	SubValidating = "validating"
	SubActive     = "active"
	SubPaused     = "paused"
	SubError      = "error"
	SubDeleted    = "deleted"
)

// Delivery modes.
const (
	ModeFanout    = "fanout"
	ModeExclusive = "exclusive"
	ModeObserve   = "observe"
)

var (
	subIntervals = map[string]time.Duration{"30s": 30 * time.Second, "1m": time.Minute, "2m": 2 * time.Minute}
	subPolicies  = map[string]bool{"": true, "draft": true, "approval": true, "auto": true, "none": true}
	subModes     = map[string]bool{"": true, ModeFanout: true, ModeExclusive: true, ModeObserve: true}
	subActs      = map[string]bool{"": true, "mention": true, "always": true}
	subEvents    = map[string]bool{"message": true, "reaction": true, "updated": true, "deleted": true}
	subStatuses  = map[string]bool{"": true, SubValidating: true, SubActive: true, SubPaused: true, SubError: true, SubDeleted: true}
)

// IntervalDuration is the subscription's read interval (1m by default).
func (s Subscription) IntervalDuration() time.Duration {
	if d, ok := subIntervals[s.Interval]; ok {
		return d
	}
	return time.Minute
}

// EffectiveMode resolves the default mode.
func (s Subscription) EffectiveMode() string {
	if s.Mode != "" {
		return s.Mode
	}
	if s.ResponsePolicy == "none" {
		return ModeFanout
	}
	return ModeExclusive
}

// EffectiveStatus is Status with its default (validating).
func (s Subscription) EffectiveStatus() string {
	if s.Status == "" {
		return SubValidating
	}
	return s.Status
}

// EventKinds is Events with its default.
func (s Subscription) EventKinds() []string {
	if len(s.Events) == 0 {
		return []string{"message"}
	}
	return s.Events
}

// Matches reports whether an event on (channel, chat) of this kind is for
// this subscription. It does not look at status, sender or activation.
func (s Subscription) Matches(channel, chat, kind string) bool {
	if s.Channel != channel {
		return false
	}
	if s.Chat != "*" && s.Chat != chat {
		return false
	}
	if kind == "" {
		kind = "message"
	}
	for _, k := range s.EventKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// AllowsSender applies the allow list: empty is nobody, "*" is everyone.
func (s Subscription) AllowsSender(sender string) bool {
	for _, a := range s.Allow {
		if a == "*" || (a != "" && a == sender) {
			return true
		}
	}
	return false
}

// ValidateSubscription is the static check: the fields are well formed. That
// the channel and the workflow exist is the engine's check, because only it
// knows which workflows are loaded.
func ValidateSubscription(s Subscription) error {
	var errs []string
	if s.Name == "" {
		errs = append(errs, "a subscription needs a name")
	} else if strings.ContainsAny(s.Name, " /\\\t\n") {
		errs = append(errs, "name may not contain spaces or slashes")
	}
	if s.Channel == "" {
		errs = append(errs, "channel is required")
	}
	if s.Chat == "" {
		errs = append(errs, "chat is required (a chat id, or * for every chat)")
	}
	if s.Workflow == "" {
		errs = append(errs, "workflow is required")
	}
	if _, ok := subIntervals[s.Interval]; s.Interval != "" && !ok {
		errs = append(errs, fmt.Sprintf("interval must be 30s, 1m or 2m, got %q", s.Interval))
	}
	if !subPolicies[s.ResponsePolicy] {
		errs = append(errs, fmt.Sprintf("responsePolicy must be draft, approval, auto or none, got %q", s.ResponsePolicy))
	}
	if !subModes[s.Mode] {
		errs = append(errs, fmt.Sprintf("mode must be fanout, exclusive or observe, got %q", s.Mode))
	}
	if !subActs[s.Activation] {
		errs = append(errs, fmt.Sprintf("activation must be mention or always, got %q", s.Activation))
	}
	if !subStatuses[s.Status] {
		errs = append(errs, fmt.Sprintf("unknown status %q", s.Status))
	}
	for _, e := range s.Events {
		if !subEvents[e] {
			errs = append(errs, fmt.Sprintf("unknown event %q (message, reaction, updated, deleted)", e))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// SubscriptionList is every subscription, sorted by name.
func (c *Catalog) SubscriptionList() []Subscription {
	out := make([]Subscription, 0, len(c.Subscriptions))
	for _, s := range c.Subscriptions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReadSubscription reads one subscription straight from disk.
func ReadSubscription(registriesPath, name string) (Subscription, bool, error) {
	f, err := readFile(registriesPath)
	if err != nil {
		return Subscription{}, false, err
	}
	s, ok := f.Subscriptions[name]
	s.Name = name
	return s, ok, nil
}

// SaveSubscription writes or replaces one subscription after the static
// check. A subscription is kept even when it fails later checks: its status
// says error and the router skips it.
func SaveSubscription(registriesPath string, s Subscription) error {
	if err := ValidateSubscription(s); err != nil {
		return err
	}
	return mutate(registriesPath, func(f *file) {
		if f.Subscriptions == nil {
			f.Subscriptions = map[string]Subscription{}
		}
		stored := s
		stored.Name = ""
		f.Subscriptions[s.Name] = stored
	})
}

// DeleteSubscription removes a subscription from the file.
func DeleteSubscription(registriesPath, name string) error {
	return mutate(registriesPath, func(f *file) { delete(f.Subscriptions, name) })
}
