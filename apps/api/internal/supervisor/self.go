package supervisor

import (
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
	req, err := http.NewRequestWithContext(ctx, method, s.URL+"/api/service-self/"+s.Key+path, nil)
	if err != nil {
		return err
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
