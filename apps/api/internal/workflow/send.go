package workflow

import (
	"fmt"
	"strings"
	"time"
)

// SendStep is a `send:` step (ADR 0023):
//
//   - id: tell-ops
//     send:
//     channel: ops              # omitted ⇒ the server's default channel
//     text: "{{ .Steps.check.summary }}"
//     attachment: "{{ .Steps.report.path }}"   # a path or an http(s) URL
//     dedup:
//     key: "disk-full-{{ .Input.host }}"
//     window: 6h
//     dry_run: true             # say what would be sent, send nothing
//
// The text, attachment, target and dedup key are templates over the same data
// a prompt sees.
type SendStep struct {
	Channel    string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Text       string `yaml:"text" json:"text"`
	Attachment string `yaml:"attachment,omitempty" json:"attachment,omitempty"`
	// Target overrides the channel's own address; opaque to wfnexus.
	Target string `yaml:"target,omitempty" json:"target,omitempty"`
	Dedup  *Dedup `yaml:"dedup,omitempty" json:"dedup,omitempty"`
	// AttachmentKind is file (default), image, voice or sticker.
	AttachmentKind string `yaml:"attachment_kind,omitempty" json:"attachmentKind,omitempty"`
	// ReplyTo quote-replies to a message id; ReplyToSender is its sender.
	ReplyTo       string `yaml:"reply_to,omitempty" json:"replyTo,omitempty"`
	ReplyToSender string `yaml:"reply_to_sender,omitempty" json:"replyToSender,omitempty"`
	// Mentions is a comma-separated list of ids to @mention.
	Mentions string `yaml:"mentions,omitempty" json:"mentions,omitempty"`
	// Reaction reacts to a message instead of posting.
	Reaction *SendReaction `yaml:"reaction,omitempty" json:"reaction,omitempty"`
	// Edit / Delete name one of OUR sent messages; for an edit Text is the
	// new text.
	Edit   string `yaml:"edit,omitempty" json:"edit,omitempty"`
	Delete string `yaml:"delete,omitempty" json:"delete,omitempty"`
	// DryRun renders and records the message without running the adapter.
	DryRun bool `yaml:"dry_run,omitempty" json:"dryRun,omitempty"`
}

// SendReaction is a send step's reaction. Every field is a template; a
// target that renders empty means "no reaction", an emoji that renders empty
// removes ours.
type SendReaction struct {
	Target string `yaml:"target" json:"target"`
	Emoji  string `yaml:"emoji" json:"emoji"`
	Sender string `yaml:"sender,omitempty" json:"sender,omitempty"`
}

// Dedup suppresses a second send under the same key within Window. A window
// of 0 means "once, ever" for that key.
type Dedup struct {
	Key    string `yaml:"key" json:"key"`
	Window string `yaml:"window,omitempty" json:"window,omitempty"`
}

// WindowDuration parses Window; "" is 0.
func (d Dedup) WindowDuration() (time.Duration, error) {
	if d.Window == "" {
		return 0, nil
	}
	return time.ParseDuration(d.Window)
}

func (s *SendStep) validate() error {
	if strings.TrimSpace(s.Text) == "" && s.Attachment == "" && s.Reaction == nil && s.Delete == "" {
		return fmt.Errorf("needs `text` (or an `attachment`, a `reaction` or a `delete`)")
	}
	if s.Reaction != nil && strings.TrimSpace(s.Reaction.Target) == "" {
		return fmt.Errorf("reaction needs a `target` message id")
	}
	switch s.AttachmentKind {
	case "", "file", "image", "voice", "sticker":
	default:
		if !strings.Contains(s.AttachmentKind, "{{") {
			return fmt.Errorf("attachment_kind must be file, image, voice or sticker")
		}
	}
	if s.Dedup != nil {
		if strings.TrimSpace(s.Dedup.Key) == "" {
			return fmt.Errorf("dedup needs a `key`")
		}
		if _, err := s.Dedup.WindowDuration(); err != nil {
			return fmt.Errorf("dedup window: %w", err)
		}
	}
	return nil
}

// SendOutputSchema is the fixed output of a `send:` step, so a later step or
// a gate can branch on whether it went out.
func SendOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"sent", "channel"},
		"properties": map[string]any{
			"sent":    map[string]any{"type": "boolean", "description": "the adapter accepted it"},
			"skipped": map[string]any{"type": "string", "description": "why nothing was sent: dedup | dry_run"},
			"channel": map[string]any{"type": "string"},
			"id":      map[string]any{"type": "string", "description": "the adapter's message id, if it gave one"},
			"text":    map[string]any{"type": "string"},
			"action":  map[string]any{"type": "string", "description": "text | reply | media | reaction | edit | delete"},
		},
	}
}
