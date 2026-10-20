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
	// DryRun renders and records the message without running the adapter.
	DryRun bool `yaml:"dry_run,omitempty" json:"dryRun,omitempty"`
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
	if strings.TrimSpace(s.Text) == "" && s.Attachment == "" {
		return fmt.Errorf("needs `text` (or an `attachment`)")
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
		},
	}
}
