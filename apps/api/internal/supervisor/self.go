package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The service's own side. The supervisor starts every service with these
// three env vars; a service uses them to read its watches from wfnexus and to
// heartbeat from INSIDE its loop, so a process that is alive but stuck stops
// beating and shows as silent.
const (
	EnvURL   = "WFX_URL"
	EnvKey   = "WFX_SERVICE"       // <module>/<account>
	EnvToken = "WFX_SERVICE_TOKEN" // scoped to this one service
)

// Self is a running service's client to wfnexus.
type Self struct {
	URL, Key, Token string
	HTTP            *http.Client
}

// FromEnv reads the three variables the supervisor sets.
func FromEnv() (*Self, error) {
	s := &Self{URL: strings.TrimRight(os.Getenv(EnvURL), "/"), Key: os.Getenv(EnvKey), Token: os.Getenv(EnvToken)}
	if s.URL == "" || s.Key == "" || s.Token == "" {
		return nil, fmt.Errorf("%s, %s and %s must be set — start this through `wfx module start`", EnvURL, EnvKey, EnvToken)
	}
	return s, nil
}

func (s *Self) do(ctx context.Context, method, path string, out any) error {
	return s.doBody(ctx, method, path, nil, out)
}

func (s *Self) doBody(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.URL+"/api/service-self/"+s.Key+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Watches decodes this service's registered watches into out (a slice).
func (s *Self) Watches(ctx context.Context, out any) error {
	var body struct {
		Watches json.RawMessage `json:"watches"`
	}
	if err := s.do(ctx, http.MethodGet, "/watches", &body); err != nil {
		return err
	}
	if len(body.Watches) == 0 {
		return errors.New("no watches in reply")
	}
	return json.Unmarshal(body.Watches, out)
}

// Beat says the service's own loop is alive.
func (s *Self) Beat(ctx context.Context) error {
	return s.do(ctx, http.MethodPost, "/heartbeat", nil)
}

// OutboxItem is one op queued for this service (a send through it, #109).
type OutboxItem struct {
	ID      string          `json:"id"`
	Request json.RawMessage `json:"request"`
}

// Outbox is what this service should send now, oldest first.
func (s *Self) Outbox(ctx context.Context) ([]OutboxItem, error) {
	var body struct {
		Items []OutboxItem `json:"items"`
	}
	return body.Items, s.do(ctx, http.MethodGet, "/outbox", &body)
}

// Report says what happened to one item: sent, failed or uncertain.
func (s *Self) Report(ctx context.Context, id, status string, result json.RawMessage) error {
	return s.doBody(ctx, http.MethodPost, "/outbox/"+id, map[string]any{"status": status, "result": result}, nil)
}
