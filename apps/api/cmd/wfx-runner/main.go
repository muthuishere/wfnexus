// Command wfx-runner joins a machine to a wfnexus pool and runs the steps sent
// to it.
//
// It is the whole of what you install on somebody's Windows box, a build VM, a
// Jenkins node or a laptop: one binary, one command.
//
//	wfx-runner join --url https://wfx.example.com --token wfx_… --labels windows,devin
//	wfx-runner run
//
// The direction matters. The platform never connects to this machine — this
// machine connects out and asks for work. So the platform can live in
// Kubernetes behind an ingress and still run a step here, with the Devin or
// Claude CLI installed HERE, with this machine's credentials, and nothing has
// to be opened, forwarded or exposed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// version travels with the worker so the Workers page can say what is out there.
var version = "dev"

// errRemoved is the platform saying this worker is not in the pool. It is the
// one refusal that waiting cannot fix, so it ends the loop rather than joining
// the backoff with the transport failures.
var errRemoved = errors.New("worker not registered")

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "join":
		err = cmdJoin(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "leave":
		err = cmdLeave(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`wfx-runner — run wfnexus workflow steps on this machine

  wfx-runner join --url URL --token TOKEN [--labels a,b] [--name NAME]
      Join the pool. Copy this line from the platform's Workers page.
      Add --run to start working straight away.

  wfx-runner run [--once]
      Ask for work and do it, forever. This is what a service runs.

  wfx-runner status      What this machine joined, and whether it is reachable.
  wfx-runner leave       Forget the registration on this machine.

Configuration lives in `)
	fmt.Println(configPath() + ".")
}

// ---- what this machine remembers ----

// Config is what `join` leaves behind: the address, this worker's own token,
// and where it keeps checkouts. The registration token is NOT kept — it was
// only needed to join.
type Config struct {
	URL      string   `json:"url"`
	Token    string   `json:"token"`
	WorkerID string   `json:"workerId"`
	Name     string   `json:"name"`
	Labels   []string `json:"labels"`
	WorkDir  string   `json:"workDir"`
}

func configDir() string {
	if v := os.Getenv("WFX_RUNNER_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".wfx-runner"
	}
	return filepath.Join(home, ".wfx-runner")
}

func configPath() string { return filepath.Join(configDir(), "config.json") }

func loadConfig() (*Config, error) {
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("this machine has not joined a pool yet — run `wfx-runner join`")
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w", configPath(), err)
	}
	return &c, nil
}

func saveConfig(c *Config) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file holds this worker's token, and anything that can read it
	// can take this machine's work.
	return os.WriteFile(configPath(), b, 0o600)
}

// ---- join ----

func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	url := fs.String("url", "", "platform URL, e.g. https://wfx.example.com")
	token := fs.String("token", "", "registration token from the Workers page")
	labels := fs.String("labels", "self-hosted", "comma-separated labels this machine serves")
	name := fs.String("name", "", "worker name (default: this machine's hostname)")
	workDir := fs.String("work", "", "where checkouts go (default: "+filepath.Join(configDir(), "work")+")")
	start := fs.Bool("run", false, "start working immediately after joining")
	_ = fs.Parse(args)

	if *url == "" || *token == "" {
		return errors.New("--url and --token are required — copy the line from the Workers page")
	}
	// Plain HTTP is supported on purpose: most installs are an internal address
	// with no certificate, and refusing them would only teach people to skip
	// verification elsewhere. It is said out loud because the token is a bearer
	// credential and so is the one this machine gets back.
	if strings.HasPrefix(strings.ToLower(*url), "http://") {
		fmt.Fprintln(os.Stderr, "note: joining over plain http — the token crosses the network in clear. Fine on a trusted network; use https over anything else.")
	}
	who := *name
	if who == "" {
		who, _ = os.Hostname()
	}
	body, _ := json.Marshal(map[string]any{
		"token": *token, "name": who, "labels": splitList(*labels),
		"os": runtime.GOOS, "arch": runtime.GOARCH, "version": version,
	})
	var res struct {
		Worker struct {
			ID     string   `json:"id"`
			Labels []string `json:"labels"`
		} `json:"worker"`
		Token    string   `json:"token"`
		Shadowed []string `json:"shadowed"`
	}
	if err := call(context.Background(), http.MethodPost,
		strings.TrimRight(*url, "/")+"/api/workers/join", "", bytes.NewReader(body), &res); err != nil {
		return err
	}
	cfg := &Config{
		URL: strings.TrimRight(*url, "/"), Token: res.Token, WorkerID: res.Worker.ID,
		Name: who, Labels: res.Worker.Labels,
		WorkDir: *workDir,
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(configDir(), "work")
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("joined %s as %q (%s/%s), serving: %s\n",
		cfg.URL, cfg.Name, runtime.GOOS, runtime.GOARCH, strings.Join(cfg.Labels, ", "))
	if len(res.Shadowed) > 0 {
		// Otherwise this machine sits there online and idle while the platform
		// quietly does the work itself, which looks like a broken worker.
		fmt.Fprintf(os.Stderr,
			"warning: the platform serves %s itself, so nothing with %s will be sent here. "+
				"Re-join with a label only this machine has.\n",
			strings.Join(res.Shadowed, ", "),
			map[bool]string{true: "those labels", false: "that label"}[len(res.Shadowed) > 1])
	}
	if *start {
		return cmdRun(nil)
	}
	fmt.Println("now run:  wfx-runner run")
	return nil
}

func cmdStatus(_ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	fmt.Printf("%s  →  %s\n  labels: %s\n  work:   %s\n",
		cfg.Name, cfg.URL, strings.Join(cfg.Labels, ", "), cfg.WorkDir)
	if err := call(context.Background(), http.MethodPost, cfg.URL+"/api/workers/heartbeat", cfg.Token, nil, nil); err != nil {
		fmt.Println("  reachable: no —", err)
		return nil
	}
	fmt.Println("  reachable: yes")
	return nil
}

func cmdLeave(_ []string) error {
	if err := os.Remove(configPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("this machine has left the pool; remove it from the Workers page too")
	return nil
}

// ---- the loop ----

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	once := fs.Bool("once", false, "take at most one job, then exit")
	_ = fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("%s serving %s — waiting for work from %s\n", cfg.Name, strings.Join(cfg.Labels, ", "), cfg.URL)
	backoff := time.Second
	for ctx.Err() == nil {
		job, err := claim(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// A removed worker must EXIT, not retry. Backing off forever
			// leaves a process nobody is watching holding whatever started
			// it — a service slot, a scheduled task, an agentbus job — and
			// the machine looks busy while doing nothing at all.
			if errors.Is(err, errRemoved) {
				return fmt.Errorf("this worker is no longer in the pool — run `wfx-runner join` again")
			}
			// The platform restarting, or a laptop that closed its lid, must not
			// end the worker — it backs off and keeps asking.
			fmt.Fprintln(os.Stderr, "waiting:", err)
			sleep(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if job == nil {
			continue // the long poll expired with nothing queued
		}
		execute(ctx, cfg, job)
		if *once {
			return nil
		}
	}
	fmt.Println("stopped")
	return nil
}

// job mirrors the queue row; only the payload matters here.
type job struct {
	ID      string  `json:"id"`
	StepID  string  `json:"stepId"`
	Label   string  `json:"label"`
	Payload payload `json:"payload"`
}

type payload struct {
	Kind       string            `json:"kind"`
	RunID      string            `json:"runId"`
	StepID     string            `json:"stepId"`
	Project    string            `json:"project"`
	Command    string            `json:"command"`
	Shell      string            `json:"shell"`
	RepoURL    string            `json:"repoUrl"`
	Ref        string            `json:"ref"`
	Env        map[string]string `json:"env"`
	TimeoutSec int               `json:"timeoutSec"`
	// Agent is a whole agent step — its skills, tools, guardrails, budget and
	// output schema — to run here. The step's CLI is NOT carried: a provider
	// naming `devin` means the devin on this machine's PATH, with this
	// machine's credential. That is the point of putting a worker here.
	Agent *engine.AgentJob `json:"agent"`
}

type result struct {
	OK       bool                 `json:"ok"`
	ExitCode int                  `json:"exitCode"`
	Stdout   string               `json:"stdout"`
	Stderr   string               `json:"stderr"`
	Error    string               `json:"error,omitempty"`
	Agent    *engine.AgentOutcome `json:"agent,omitempty"`
}

func claim(ctx context.Context, cfg *Config) (*job, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/api/workers/claim?wait=25s", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil // nothing queued; ask again
	case http.StatusOK:
		var j job
		return &j, json.NewDecoder(resp.Body).Decode(&j)
	case http.StatusUnauthorized:
		// Removed from the pool, or the platform's registration was reset.
		// Reported as a distinct condition rather than a transport error,
		// because it never resolves by waiting.
		return nil, errRemoved
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("claim: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
}

// execute does one job and reports, whatever happens. A worker that dies
// without reporting is handled by the platform requeueing the job, but a worker
// that is merely confused should say so rather than go quiet.
func execute(ctx context.Context, cfg *Config, j *job) {
	var res result
	if j.Payload.Kind == "agent" && j.Payload.Agent != nil {
		fmt.Printf("• %s %s: agent (%s)\n", short(j.Payload.RunID), j.Payload.StepID, providerOf(j.Payload.Agent))
		res = doAgent(ctx, cfg, j)
	} else {
		fmt.Printf("• %s %s: %s\n", short(j.Payload.RunID), j.Payload.StepID, firstLine(j.Payload.Command))
		res = do(ctx, cfg, j.Payload)
	}
	switch {
	case res.Error != "":
		fmt.Printf("  ! %s\n", res.Error)
	case res.Agent != nil:
		fmt.Printf("  %d turns, %d tokens\n", res.Agent.Turns, res.Agent.TotalTokens)
	default:
		fmt.Printf("  exit %d\n", res.ExitCode)
	}
	body, _ := json.Marshal(res)
	// Reported with a context that survives Ctrl-C: the work is done, and
	// throwing away the result would make the run wait for a repeat of it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := call(rctx, http.MethodPost,
		cfg.URL+"/api/workers/jobs/"+j.ID+"/result", cfg.Token, bytes.NewReader(body), nil); err != nil {
		fmt.Fprintln(os.Stderr, "could not report result:", err)
	}
}

// doAgent runs a whole agent step here. The engine package is the SAME code
// the platform runs, given no database and a sink that posts the agent's
// activity back — so a step cannot mean one thing on the server and another on
// this machine.
func doAgent(ctx context.Context, cfg *Config, j *job) result {
	dir, err := workspace(ctx, cfg, j.Payload)
	if err != nil {
		return result{Error: err.Error()}
	}
	// Events are posted back in small batches while the step runs, so the live
	// view of a step executing here is the same view as one executing there.
	post := newEventPoster(ctx, cfg, j.ID)
	defer post.flush()

	eng := engine.NewWorkerEngine(func(kind string, ev any) { post.add(kind, ev) })
	if j.Payload.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(j.Payload.TimeoutSec)*time.Second)
		defer cancel()
	}
	out := eng.RunAgentJob(ctx, j.Payload.Agent, dir)
	return result{OK: out.Error == "", Agent: &out}
}

// providerOf names the CLI or endpoint this step will use here, for the log.
func providerOf(a *engine.AgentJob) string {
	if a.Provider != nil {
		return a.Provider.Name
	}
	if a.Step.Model != "" {
		return a.Step.Model
	}
	return a.Defaults.Model
}

func do(ctx context.Context, cfg *Config, p payload) result {
	dir, err := workspace(ctx, cfg, p)
	if err != nil {
		return result{Error: err.Error()}
	}
	sh, err := resolveShell(p.Shell)
	if err != nil {
		return result{Error: err.Error()}
	}
	if p.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutSec)*time.Second)
		defer cancel()
	}
	argv := sh.Command(p.Command)
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	// The step's env is resolved HERE, against this machine. A value like
	// ${GITHUB_PAT} names a variable this box holds; the platform never had it
	// and it never crossed the network.
	stepEnv, err := workflow.ResolveEnv(p.Env)
	if err != nil {
		return result{Error: err.Error()}
	}
	c.Env = append(os.Environ(), stepEnv...)
	// The run's identity is in the environment, so a step can tell where it is
	// and a tool on this machine can tag what it produced.
	c.Env = append(c.Env,
		"WFX_RUN_ID="+p.RunID, "WFX_STEP_ID="+p.StepID,
		"WFX_PROJECT="+p.Project, "WFX_WORKSPACE="+dir, "WFX_RUNNER="+cfg.Name)

	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			// Could not start at all — a fault of this machine, not the
			// command's verdict, and the platform must be able to tell them
			// apart or it will retry a missing interpreter forever.
			return result{Error: fmt.Sprintf("could not run %q: %v", p.Command, runErr)}
		}
	}
	return result{
		OK: code == 0, ExitCode: code,
		Stdout: cap64k(stdout.String()), Stderr: cap64k(stderr.String()),
	}
}

// resolveShell picks the interpreter: what the step named, else the best one
// this machine has. The same workflow therefore runs on Windows and Linux
// without saying so — the step says `run:`, the machine says how.
func resolveShell(name string) (shell.Shell, error) {
	if name != "" {
		return shell.Lookup(name)
	}
	return shell.Default()
}

// workspace is the directory the command runs in: a checkout of the project
// when the platform sent one, else a plain per-run directory. It is kept
// between runs of the same project so a second step is not a second clone.
func workspace(ctx context.Context, cfg *Config, p payload) (string, error) {
	base := cfg.WorkDir
	if p.RepoURL == "" {
		dir := filepath.Join(base, "runs", safeName(p.RunID))
		return dir, os.MkdirAll(dir, 0o755)
	}
	dir := filepath.Join(base, "repos", safeName(p.Project))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		if out, err := git(ctx, filepath.Dir(dir), "clone", p.RepoURL, dir); err != nil {
			return "", fmt.Errorf("clone %s: %v: %s", p.RepoURL, err, out)
		}
	}
	if _, err := git(ctx, dir, "fetch", "--all", "--prune"); err != nil {
		// A fetch that fails is worth saying but not worth failing on: the
		// checkout that is already here may be exactly the ref wanted.
		fmt.Fprintln(os.Stderr, "  (fetch failed, using the checkout as it is)")
	}
	if p.Ref != "" {
		if out, err := git(ctx, dir, "checkout", "--force", p.Ref); err != nil {
			return "", fmt.Errorf("checkout %s: %v: %s", p.Ref, err, out)
		}
	}
	return dir, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// eventPoster batches a running agent's activity back to the platform. Batched
// because an agent emits several events per turn and one request each would
// spend more time on HTTP than on work; flushed on a timer so the live view
// does not lag behind a long turn.
type eventPoster struct {
	ctx   context.Context
	cfg   *Config
	jobID string
	mu    sync.Mutex
	buf   []map[string]any
	last  time.Time
}

func newEventPoster(ctx context.Context, cfg *Config, jobID string) *eventPoster {
	return &eventPoster{ctx: ctx, cfg: cfg, jobID: jobID, last: time.Now()}
}

func (p *eventPoster) add(_ string, ev any) {
	m, ok := ev.(map[string]any)
	if !ok {
		return
	}
	p.mu.Lock()
	p.buf = append(p.buf, m)
	due := len(p.buf) >= 20 || time.Since(p.last) > time.Second
	p.mu.Unlock()
	if due {
		p.flush()
	}
}

func (p *eventPoster) flush() {
	p.mu.Lock()
	batch := p.buf
	p.buf, p.last = nil, time.Now()
	p.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{"events": batch})
	if err != nil {
		return
	}
	// A dropped event must never fail the step: the log is how you watch the
	// work, not the work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 10*time.Second)
	defer cancel()
	_ = call(ctx, http.MethodPost, p.cfg.URL+"/api/workers/jobs/"+p.jobID+"/events", p.cfg.Token,
		bytes.NewReader(body), nil)
}

// ---- plumbing ----

func call(ctx context.Context, method, url, token string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := strings.TrimSpace(string(b))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// safeName keeps a project or run id usable as one path segment on every OS.
func safeName(s string) string {
	if s == "" {
		return "run"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
}

// short is the first block of a uuid — enough to match a run in the dashboard.
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func cap64k(s string) string {
	const max = 64 << 10
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… truncated"
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
