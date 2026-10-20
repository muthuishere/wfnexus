package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// A context with `via: ssh://user@host[:port]` reaches its server through an
// SSH tunnel, the way a Docker context with host=ssh:// does. The server keeps
// listening on loopback only; nothing is exposed to the internet but sshd.
//
// The tunnel is LAZY: the first command that needs it starts a detached helper
// (`wfx tunnel serve <context>`) that listens on 127.0.0.1:<random> and
// forwards each connection over one SSH session (direct-tcpip). Every later
// command reuses it. With no open connection for idleClose the helper exits.

const idleClose = 15 * time.Minute

// viaSpec is the parsed `via` value. An optional ?key=<path> names the private
// key, so the context keeps ONE field.
type viaSpec struct {
	User string
	Addr string // host:port
	Key  string
}

func parseVia(raw string) (viaSpec, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ssh" || u.Hostname() == "" {
		return viaSpec{}, fmt.Errorf("via %q: want ssh://user@host[:port]", raw)
	}
	v := viaSpec{Addr: u.Host, Key: u.Query().Get("key")}
	if u.Port() == "" {
		v.Addr = net.JoinHostPort(u.Hostname(), "22")
	}
	if u.User != nil {
		v.User = u.User.Username()
	}
	if v.User == "" {
		v.User = os.Getenv("USER")
	}
	return v, nil
}

// forwardTarget is host:port of the context URL, as seen from the SSH host.
func forwardTarget(ctxURL string) (string, error) {
	u, err := url.Parse(ctxURL)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("context url %q has no host", ctxURL)
	}
	if u.Port() != "" {
		return u.Host, nil
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443"), nil
	}
	return net.JoinHostPort(u.Hostname(), "80"), nil
}

// ---- ssh client --------------------------------------------------------------

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func knownHostsPath() string {
	if p := os.Getenv("WFX_KNOWN_HOSTS"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), ".ssh", "known_hosts")
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

// authMethods: the named key only, when one is given; otherwise ssh-agent, then
// ~/.ssh/id_ed25519 and id_rsa. Passphrase-protected key files are skipped —
// put such a key in the agent.
func authMethods(v viaSpec) []ssh.AuthMethod {
	var signers []ssh.Signer
	files := []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa"}
	if v.Key != "" {
		files = []string{v.Key}
	} else if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			if s, err := agent.NewClient(c).Signers(); err == nil {
				signers = append(signers, s...)
			}
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(expandHome(f))
		if err != nil {
			continue
		}
		if s, err := ssh.ParsePrivateKey(raw); err == nil {
			signers = append(signers, s)
		}
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}
}

// errUnknownHost carries the key that was offered, so `context add` can show
// its fingerprint and record it on --accept-host-key.
type errUnknownHost struct {
	addr string
	key  ssh.PublicKey
}

func (e *errUnknownHost) Error() string {
	return fmt.Sprintf("unknown SSH host %s (%s %s) — check the fingerprint, then rerun with --accept-host-key",
		e.addr, e.key.Type(), ssh.FingerprintSHA256(e.key))
}

// hostKeyCallback is strict ~/.ssh/known_hosts checking. A CHANGED key is
// always refused; an unknown one is refused with errUnknownHost unless accept
// is set, in which case it is appended to known_hosts.
func hostKeyCallback(accept bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		path := knownHostsPath()
		if _, err := os.Stat(path); err == nil {
			cb, err := knownhosts.New(path)
			if err != nil {
				return err
			}
			err = cb(hostname, remote, key)
			var ke *knownhosts.KeyError
			if err == nil || !errors.As(err, &ke) || len(ke.Want) > 0 {
				return err // nil, a non-key error, or a MISMATCH
			}
		}
		if !accept {
			return &errUnknownHost{addr: hostname, key: key}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}
}

func dialSSH(v viaSpec, accept bool) (*ssh.Client, error) {
	return ssh.Dial("tcp", v.Addr, &ssh.ClientConfig{
		User:            v.User,
		Auth:            authMethods(v),
		HostKeyCallback: hostKeyCallback(accept),
		Timeout:         15 * time.Second,
	})
}

// ---- the forwarder -----------------------------------------------------------

// forwarder accepts on a local listener and forwards each connection over SSH.
// dial and idle are injectable so tests can use an in-process server and a
// short idle close.
//
// With http set (a context URL that is plain http) it does not pipe each local
// connection to its own SSH channel: it is a reverse proxy that keeps a pool
// of SSH channels open to the server and reuses them, request after request.
// Opening a channel is a round trip, and the server-side TCP connection it
// brings starts cold, so per-connection forwarding cost ~3 RTT per curl or
// fresh browser connection where a reused channel costs 1.
type forwarder struct {
	dial   func() (*ssh.Client, error)
	target string
	http   bool
	idle   time.Duration
	onIdle func(idleSince time.Time) // state-file hook; nil in tests

	mu        sync.Mutex
	client    *ssh.Client
	active    int
	idleSince time.Time
	timer     *time.Timer
}

func (f *forwarder) sshClient(fresh bool) (*ssh.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil && !fresh {
		return f.client, nil
	}
	if f.client != nil {
		f.client.Close()
		f.client = nil
	}
	c, err := f.dial()
	if err != nil {
		return nil, err
	}
	f.client = c
	go keepalive(c, keepaliveEvery)
	go func() { // drop the client the moment the session dies
		_ = c.Wait()
		f.mu.Lock()
		if f.client == c {
			f.client = nil
		}
		f.mu.Unlock()
	}()
	return c, nil
}

// keepaliveEvery is how often an idle SSH session is asked if it is alive.
// After a laptop sleeps, or a NAT drops the mapping, the TCP connection looks
// open and every write vanishes: without a probe, the next request hangs until
// the kernel gives up, minutes later. A probe that is not answered within
// keepaliveEvery closes the client, so the next request redials.
var keepaliveEvery = 15 * time.Second

func keepalive(c *ssh.Client, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		done := make(chan error, 1)
		go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				c.Close()
				return
			}
		case <-time.After(every):
			c.Close()
			return
		}
	}
}

// remote opens the forwarded connection, reconnecting the SSH session once if
// the existing one is dead.
func (f *forwarder) remote() (net.Conn, error) {
	c, err := f.sshClient(false)
	if err == nil {
		if rc, err := c.Dial("tcp", f.target); err == nil {
			return rc, nil
		}
	}
	c, err = f.sshClient(true)
	if err != nil {
		return nil, err
	}
	return c.Dial("tcp", f.target)
}

func (f *forwarder) markIdle() {
	f.idleSince = time.Now()
	if f.onIdle != nil {
		f.onIdle(f.idleSince)
	}
}

// serve runs until the listener closes or the forwarder has been idle for
// f.idle; it then closes the listener and returns nil.
func (f *forwarder) serve(ln net.Listener) error {
	f.mu.Lock()
	f.markIdle()
	f.timer = time.AfterFunc(f.idle, func() { ln.Close() })
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		if f.client != nil {
			f.client.Close()
		}
		f.mu.Unlock()
	}()
	var proxied *connQueue
	if f.http {
		proxied = newConnQueue(ln.Addr())
		tr := f.transport()
		defer tr.CloseIdleConnections()
		defer proxied.Close()
		srv := &http.Server{Handler: f.proxy(tr), ReadHeaderTimeout: time.Minute}
		// When the listener closes (idle, or a signal) the proxy goes with it,
		// open connections included — Serve alone would leave them running.
		defer srv.Close()
		go func() { _ = srv.Serve(proxied) }()
	}
	for {
		lc, err := ln.Accept()
		if err != nil {
			f.mu.Lock()
			idleOut := f.active == 0
			f.mu.Unlock()
			if idleOut {
				return nil
			}
			return err
		}
		f.mu.Lock()
		f.active++
		f.timer.Stop()
		if f.active == 1 && f.onIdle != nil {
			f.onIdle(time.Time{})
		}
		f.mu.Unlock()
		go func() {
			defer func() {
				f.mu.Lock()
				f.active--
				if f.active == 0 {
					f.markIdle()
					f.timer.Reset(f.idle)
				}
				f.mu.Unlock()
			}()
			if proxied != nil {
				tc := &trackedConn{Conn: lc, closed: make(chan struct{})}
				if proxied.push(tc) {
					<-tc.closed
				}
				lc.Close()
				return
			}
			defer lc.Close()
			rc, err := f.remote()
			if err != nil {
				fmt.Fprintln(os.Stderr, "tunnel:", err)
				return
			}
			defer rc.Close()
			pipe(lc, rc)
		}()
	}
}

// transport pools HTTP connections to the server, each one an SSH channel.
func (f *forwarder) transport() *http.Transport {
	return &http.Transport{
		DialContext:         func(context.Context, string, string) (net.Conn, error) { return f.remote() },
		MaxIdleConnsPerHost: 16,
		// The backstop behind keepalive: no answer at all in this long is a
		// dead session, not a slow server — the slowest real endpoint, publish,
		// is given 5 minutes by its own client.
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       5 * time.Minute,
		DisableCompression:    true, // pass the client's Accept-Encoding through, don't decode here
	}
}

// proxy forwards a request unchanged: the Host the client used is kept (the
// server builds links and checks origins from it) and no X-Forwarded-For is
// added (the server must keep seeing a loopback peer, as it did with the
// byte pipe). Event streams flush as they arrive.
func (f *forwarder) proxy(tr http.RoundTripper) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = f.target
			pr.Out.Host = pr.In.Host
		},
		Transport: tr,
		ErrorLog:  log.New(os.Stderr, "tunnel: ", 0),
	}
}

// connQueue is a net.Listener fed by the accept loop, so the http.Server sees
// exactly the connections serve counts for the idle close.
type connQueue struct {
	addr net.Addr
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newConnQueue(addr net.Addr) *connQueue {
	return &connQueue{addr: addr, ch: make(chan net.Conn), done: make(chan struct{})}
}

func (q *connQueue) push(c net.Conn) bool {
	select {
	case q.ch <- c:
		return true
	case <-q.done:
		return false
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case c := <-q.ch:
		return c, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error   { q.once.Do(func() { close(q.done) }); return nil }
func (q *connQueue) Addr() net.Addr { return q.addr }

// trackedConn reports its close, which is when the connection stops counting
// as active.
type trackedConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// ---- helper state on disk ----------------------------------------------------

type tunnelState struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Started   time.Time `json:"started"`
	IdleSince time.Time `json:"idleSince,omitempty"` // zero while a connection is open
	Error     string    `json:"error,omitempty"`
}

func tunnelsDir() string { return filepath.Join(filepath.Dir(contextsPath()), "tunnels") }

func statePath(name string) string {
	return filepath.Join(tunnelsDir(), strings.NewReplacer("/", "_", ":", "_").Replace(name)+".json")
}

func readState(name string) (*tunnelState, error) {
	raw, err := os.ReadFile(statePath(name))
	if err != nil {
		return nil, err
	}
	s := &tunnelState{}
	return s, json.Unmarshal(raw, s)
}

func writeState(name string, s *tunnelState) error {
	if err := os.MkdirAll(tunnelsDir(), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(s)
	tmp := statePath(name) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(name))
}

// liveState is the helper's state when it is up and answering on its port.
func liveState(name string) *tunnelState {
	s, err := readState(name)
	if err != nil || s.Port == 0 || s.Error != "" || !processAlive(s.PID) {
		return nil
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port), time.Second)
	if err != nil {
		return nil
	}
	c.Close()
	return s
}

// lock is a lock FILE created O_EXCL, so two commands starting at once start
// one helper. One older than 30s is a crashed holder's and is taken over.
func lock(name string) (func(), error) {
	if err := os.MkdirAll(tunnelsDir(), 0o700); err != nil {
		return nil, err
	}
	p := statePath(name) + ".lock"
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if info, serr := os.Stat(p); serr == nil && time.Since(info.ModTime()) > 30*time.Second {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s", p)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ensureTunnel returns the local base URL for a via context, starting the
// helper when none is running.
func ensureTunnel(name string, ctx *wfxContext) (string, error) {
	local := func(s *tunnelState) string {
		u, _ := url.Parse(ctx.URL)
		return fmt.Sprintf("%s://127.0.0.1:%d", or(u.Scheme, "http"), s.Port)
	}
	if s := liveState(name); s != nil {
		return local(s), nil
	}
	unlock, err := lock(name)
	if err != nil {
		return "", err
	}
	defer unlock()
	if s := liveState(name); s != nil {
		return local(s), nil
	}
	_ = os.Remove(statePath(name))
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logf, err := os.OpenFile(statePath(name)+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "tunnel", "serve", name)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "WFX_CONTEXTS="+contextsPath())
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := readState(name); err == nil {
			if s.Error != "" {
				_ = os.Remove(statePath(name))
				return "", fmt.Errorf("tunnel to %s: %s", name, s.Error)
			}
			if s.Port != 0 {
				return local(s), nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("tunnel to %s did not come up — see %s.log", name, statePath(name))
}

// ---- commands ----------------------------------------------------------------

// tunnelServe is the helper process. It dials SSH once up front so a bad key
// or host key is reported to the command that started it, not discovered later.
func tunnelServe(name string) error {
	fail := func(err error) error {
		_ = writeState(name, &tunnelState{PID: os.Getpid(), Error: err.Error()})
		return err
	}
	c, err := loadContexts()
	if err != nil {
		return fail(err)
	}
	ctx, ok := c.Contexts[name]
	if !ok || ctx.Via == "" {
		return fail(fmt.Errorf("context %s has no via", name))
	}
	v, err := parseVia(ctx.Via)
	if err != nil {
		return fail(err)
	}
	target, err := forwardTarget(ctx.URL)
	if err != nil {
		return fail(err)
	}
	f := &forwarder{dial: func() (*ssh.Client, error) { return dialSSH(v, false) }, target: target, idle: idleClose,
		http: strings.HasPrefix(ctx.URL, "http://")}
	if _, err := f.sshClient(false); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	st := &tunnelState{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Started: time.Now()}
	f.onIdle = func(t time.Time) { st.IdleSince = t; _ = writeState(name, st) }
	if err := writeState(name, st); err != nil {
		return err
	}
	stopOnSignal(ln, func() { os.Remove(statePath(name)) })
	defer os.Remove(statePath(name))
	return f.serve(ln)
}

func tunnelCmd(args []string) error {
	switch first(args) {
	case "serve":
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx tunnel serve <context>")
		}
		return tunnelServe(args[1])
	case "stop":
		name := ""
		if len(args) > 1 {
			name = args[1]
		} else if c, err := loadContexts(); err == nil {
			name = c.Current
		}
		s, err := readState(name)
		if err != nil {
			fmt.Printf("%s · tunnel down\n", name)
			return nil
		}
		killProcess(s.PID)
		_ = os.Remove(statePath(name))
		fmt.Printf("%s · tunnel closed\n", name)
		return nil
	case "", "status":
		return statusCmd(nil)
	}
	return fmt.Errorf("usage: wfx tunnel [status|stop [context]]")
}

// tunnelLine is the one-line tunnel state of a via context.
func tunnelLine(name string) string {
	s := liveState(name)
	if s == nil {
		return name + " · tunnel down (opens on next command)"
	}
	line := fmt.Sprintf("%s · tunnel up %s", name, roundDur(time.Since(s.Started)))
	if s.IdleSince.IsZero() {
		return line + " · in use"
	}
	left := idleClose - time.Since(s.IdleSince)
	if left < 0 {
		left = 0
	}
	return line + " · idle close in " + roundDur(left)
}

func roundDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// statusCmd is `wfx status`: the current context, and its tunnel when it has one.
func statusCmd(args []string) error {
	c, err := loadContexts()
	if err != nil {
		return err
	}
	name := c.Current
	if len(args) > 0 {
		name = args[0]
	}
	ctx, ok := c.Contexts[name]
	if !ok {
		fmt.Println("no current context — default " + base())
		return nil
	}
	if ctx.Via == "" {
		fmt.Printf("%s · %s\n", name, ctx.URL)
		return nil
	}
	fmt.Println(tunnelLine(name))
	return nil
}

// contextAdd is `wfx context add <name> --url U [--via ssh://…] [--key K] [--accept-host-key]`.
func contextAdd(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: wfx context add <name> --url http://127.0.0.1:8090 [--via ssh://user@host] [--key ~/.ssh/id_ed25519] [--accept-host-key]")
	}
	name := args[0]
	u := normalizeURL(flagOf(args, "--url", "http://127.0.0.1:8090"))
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	via := flagOf(args, "--via", "")
	if via != "" {
		if key := flagOf(args, "--key", ""); key != "" {
			// Unescaped, so the path stays readable in `wfx context`.
			via = strings.SplitN(via, "?", 2)[0] + "?key=" + expandHome(key)
		}
		v, err := parseVia(via)
		if err != nil {
			return err
		}
		if _, err := forwardTarget(u); err != nil {
			return err
		}
		// One handshake now: an unknown host key is shown and accepted HERE,
		// once, never silently by a background helper.
		cl, err := dialSSH(v, hasFlag(args, "--accept-host-key"))
		if err != nil {
			return err
		}
		cl.Close()
	}
	c, err := loadContexts()
	if err != nil {
		return err
	}
	prev := c.Contexts[name]
	ctx := &wfxContext{URL: u, Via: via, Created: time.Now().UTC().Format(time.RFC3339)}
	if prev != nil && prev.URL == u && prev.Via == via {
		ctx.Token, ctx.User, ctx.Project = prev.Token, prev.User, prev.Project
	}
	c.Contexts[name] = ctx
	c.Current = name
	if err := saveContexts(c); err != nil {
		return err
	}
	if via != "" {
		fmt.Printf("%s · %s via %s (tunnel opens on next command)\n", name, u, via)
	} else {
		fmt.Printf("%s · %s\n", name, u)
	}
	return nil
}
