// Package carryops drives the carry CLI (github.com/muthuishere/carry) on
// behalf of the server, so `wfx tool` / `wfx config` work against a context
// without the client ever holding the bucket's S3 credentials or the
// encryption key. It speaks carry's frozen JSON contract
// (docs/cli-contract.md v0.1): every call is `carry --json <args>`, the answer
// is one `{"ok":…,"result"|"error"}` envelope on stdout, and env values travel
// on stdin, never argv.
package carryops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured is returned when the server was started without
// CARRY_REPO. It is not a failure of carry; it is carry being off.
var ErrNotConfigured = errors.New("carry is not configured on this server: start it with CARRY_REPO (and CARRY_S3_ENDPOINT, CARRY_KEY_FILE, AWS credentials) — `wfx doctor` shows the state; docs: concepts/carry")

// ErrNotInstalled is returned when CARRY_REPO is set but no carry binary can be run.
var ErrNotInstalled = errors.New("carry is configured (CARRY_REPO set) but the carry binary is not on this server's PATH — the shipped images include it; set CARRY_BIN for a bare install")

// Error is a failed carry invocation, with carry's own error code.
type Error struct {
	Code    string
	Message string
	Exit    int
}

func (e *Error) Error() string { return fmt.Sprintf("carry %s: %s", e.Code, e.Message) }

// Client runs the carry binary.
type Client struct {
	Bin string
	// Configured mirrors `wfx doctor`'s carry line: CARRY_REPO is set.
	Configured bool
	// DefaultState is the state a request with no state names: the server's
	// own, CARRY_STATE else "wfnexus" (the project name the images use).
	DefaultState string
	Timeout      time.Duration
}

// FromEnv builds the client from this process's environment.
func FromEnv() *Client {
	bin := os.Getenv("CARRY_BIN")
	if bin == "" {
		bin = "carry"
	}
	st := os.Getenv("CARRY_STATE")
	if st == "" {
		st = "wfnexus"
	}
	return &Client{Bin: bin, Configured: os.Getenv("CARRY_REPO") != "", DefaultState: st, Timeout: 60 * time.Second}
}

// State resolves an empty state to the server's own.
func (c *Client) State(s string) string {
	if strings.TrimSpace(s) == "" {
		return c.DefaultState
	}
	return s
}

// Ready says whether calls can be made, and why not.
func (c *Client) Ready() error {
	if !c.Configured {
		return ErrNotConfigured
	}
	if _, err := exec.LookPath(c.Bin); err != nil {
		return ErrNotInstalled
	}
	return nil
}

// Run executes `carry --json args…` and returns the result payload.
func (c *Client) Run(ctx context.Context, stdin []byte, args ...string) (json.RawMessage, error) {
	if err := c.Ready(); err != nil {
		return nil, err
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Bin, append([]string{"--json"}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	exit := 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		exit = ee.ExitCode()
	} else if runErr != nil {
		return nil, &Error{Code: "internal", Message: runErr.Error(), Exit: 1}
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		// stderr is carry's log; it never holds a value (contract), but keep
		// it short.
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &Error{Code: "internal", Message: "unparseable output: " + msg, Exit: exit}
	}
	if env.OK {
		return env.Result, nil
	}
	if env.Error == nil {
		return nil, &Error{Code: "internal", Message: "unknown error", Exit: exit}
	}
	return nil, &Error{Code: env.Error.Code, Message: env.Error.Message, Exit: exit}
}

// Call is Run decoded into a generic map — the API passes carry's result
// shapes through unchanged.
func (c *Client) Call(ctx context.Context, stdin []byte, args ...string) (map[string]any, error) {
	raw, err := c.Run(ctx, stdin, args...)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Path is a carry state path: "<state>:<absolute path>".
func Path(state, p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be absolute (e.g. /home/wfx/.gitconfig)", p)
	}
	return state + ":" + p, nil
}

func (c *Client) ToolCatalog(ctx context.Context, name string) (map[string]any, error) {
	args := []string{"tool", "ls"}
	if name != "" {
		args = append(args, name)
	}
	return c.Call(ctx, nil, args...)
}

func (c *Client) ToolAdd(ctx context.Context, state, tool, channel string) (map[string]any, error) {
	args := []string{"tool", "add", c.State(state), tool}
	if channel != "" {
		args = append(args, "--channel", channel)
	}
	return c.Call(ctx, nil, args...)
}

func (c *Client) ToolRm(ctx context.Context, state, name string) (map[string]any, error) {
	return c.Call(ctx, nil, "tool", "rm", c.State(state), name)
}

func (c *Client) Ls(ctx context.Context, state, dir string) (map[string]any, error) {
	t := c.State(state)
	if dir != "" {
		t += ":" + dir
	}
	return c.Call(ctx, nil, "ls", t)
}

func (c *Client) Get(ctx context.Context, state, p string) ([]byte, error) {
	sp, err := Path(c.State(state), p)
	if err != nil {
		return nil, err
	}
	m, err := c.Call(ctx, nil, "get", sp)
	if err != nil {
		return nil, err
	}
	s, _ := m["content_base64"].(string)
	return base64.StdEncoding.DecodeString(s)
}

func (c *Client) Put(ctx context.Context, state, p string, data []byte, mode int) (map[string]any, error) {
	sp, err := Path(c.State(state), p)
	if err != nil {
		return nil, err
	}
	args := []string{"put", sp, "-"}
	if mode > 0 {
		args = append(args, "--mode", "0"+strconv.FormatInt(int64(mode), 8))
	}
	return c.Call(ctx, data, args...)
}

func (c *Client) Rm(ctx context.Context, state, p string) (map[string]any, error) {
	sp, err := Path(c.State(state), p)
	if err != nil {
		return nil, err
	}
	return c.Call(ctx, nil, "rm", sp)
}

// EnvKeys lists the names only. Values are fetched by EnvValues, which only
// the move path (sync) uses; nothing prints them.
func (c *Client) EnvKeys(ctx context.Context, state string) ([]string, error) {
	vals, err := c.EnvValues(ctx, state)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func (c *Client) EnvValues(ctx context.Context, state string) (map[string]string, error) {
	raw, err := c.Run(ctx, nil, "env", "get", c.State(state))
	if err != nil {
		return nil, err
	}
	var r struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Env == nil {
		r.Env = map[string]string{}
	}
	return r.Env, nil
}

// EnvSet sends KEY=VALUE lines over stdin (`env import <state> -`), never argv,
// so a value never shows in a process listing.
func (c *Client) EnvSet(ctx context.Context, state string, vars map[string]string) (map[string]any, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if k == "" || strings.ContainsAny(k, "=\n ") || strings.Contains(vars[k], "\n") {
			return nil, &Error{Code: "usage", Message: "invalid env entry " + strconv.Quote(k), Exit: 2}
		}
		b.WriteString(k + "=" + vars[k] + "\n")
	}
	return c.Call(ctx, []byte(b.String()), "env", "import", c.State(state), "-")
}

func (c *Client) EnvUnset(ctx context.Context, state string, keys ...string) (map[string]any, error) {
	return c.Call(ctx, nil, append([]string{"env", "unset", c.State(state)}, keys...)...)
}

func (c *Client) Log(ctx context.Context, state string, limit int) (map[string]any, error) {
	args := []string{"log", c.State(state)}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	return c.Call(ctx, nil, args...)
}

func (c *Client) Rollback(ctx context.Context, state string, version int) (map[string]any, error) {
	return c.Call(ctx, nil, "rollback", c.State(state), strconv.Itoa(version))
}

func (c *Client) States(ctx context.Context) (map[string]any, error) {
	return c.Call(ctx, nil, "states")
}
