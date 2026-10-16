package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseVia(t *testing.T) {
	cases := []struct{ in, user, addr, key string }{
		{"ssh://wfx@46.4.13.167", "wfx", "46.4.13.167:22", ""},
		{"ssh://wfx@46.4.13.167:2222", "wfx", "46.4.13.167:2222", ""},
		{"ssh://wfx@h.example.com?key=/k/id", "wfx", "h.example.com:22", "/k/id"},
	}
	for _, c := range cases {
		v, err := parseVia(c.in)
		if err != nil || v.User != c.user || v.Addr != c.addr || v.Key != c.key {
			t.Errorf("parseVia(%q) = %+v, %v", c.in, v, err)
		}
	}
	for _, bad := range []string{"http://h", "wfx@h", "ssh://"} {
		if _, err := parseVia(bad); err == nil {
			t.Errorf("parseVia(%q) should fail", bad)
		}
	}
	if got, _ := forwardTarget("http://127.0.0.1:8090"); got != "127.0.0.1:8090" {
		t.Errorf("forwardTarget = %s", got)
	}
}

// A contexts file written before `via` existed reads and resolves unchanged,
// and a context without via is written without the field.
func TestContextWithoutViaIsUnchanged(t *testing.T) {
	path := tempContexts(t)
	old := `{"current":"a.example.com","contexts":{"a.example.com":{"url":"https://a.example.com","token":"wfx_a"}}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := resolveContext("")
	if err != nil || r.url != "https://a.example.com" || r.token != "wfx_a" {
		t.Fatalf("resolve = %+v, %v", r, err)
	}
	if err := putContext("https://b.example.com", "wfx_b", "u"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"via"`) {
		t.Fatalf("via written for a direct context: %s", raw)
	}
	// A login through a via context keeps its url and via.
	c, _ := loadContexts()
	c.Contexts["deemwar"] = &wfxContext{URL: "http://127.0.0.1:8090", Via: "ssh://wfx@h"}
	_ = saveContexts(c)
	if err := putContext("deemwar", "wfx_d", "muthu"); err != nil {
		t.Fatal(err)
	}
	c, _ = loadContexts()
	if d := c.Contexts["deemwar"]; d.Via != "ssh://wfx@h" || d.URL != "http://127.0.0.1:8090" || d.Token != "wfx_d" {
		t.Fatalf("via context after login = %+v", d)
	}
	var back contextsFile
	_ = json.Unmarshal(raw, &back)
}

func testPubKey(t *testing.T) string {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sp))
}

func TestTeamAuthorizedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	own := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwnOwnOwnOwnOwnOwnOwnOwnOwnOwnOwnOwnOwnOwn admin@laptop"
	_ = os.WriteFile(path, []byte(own+"\n"), 0o600)

	k1, k2 := testPubKey(t), testPubKey(t)
	if ch, err := teamAdd(path, "alice", k1, "127.0.0.1:8090"); err != nil || !ch {
		t.Fatal(ch, err)
	}
	if ch, _ := teamAdd(path, "alice", k1, "127.0.0.1:8090"); ch {
		t.Fatal("second identical add should be a no-op")
	}
	if ch, _ := teamAdd(path, "alice", k2, "127.0.0.1:8090"); !ch {
		t.Fatal("new key should replace")
	}
	_, _ = teamAdd(path, "bob", k1, "127.0.0.1:8090")
	if _, err := teamAdd(path, "eve", "not a key", "127.0.0.1:8090"); err == nil {
		t.Fatal("garbage key accepted")
	}
	lines, _ := readLines(path)
	if len(lines) != 3 || lines[0] != own {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, `restrict,port-forwarding,permitopen="127.0.0.1:8090" ssh-ed25519 `) {
			t.Fatalf("bad line %q", l)
		}
	}
	if strings.Contains(strings.Join(lines, "\n"), strings.Fields(k1)[1]+" wfx-team:alice") {
		t.Fatal("alice still has the old key")
	}
	if ch, _ := teamRemove(path, "alice"); !ch {
		t.Fatal("remove")
	}
	if ch, _ := teamRemove(path, "alice"); ch {
		t.Fatal("second remove should be a no-op")
	}
	lines, _ = readLines(path)
	if len(lines) != 2 || lineTeam(lines[1]) != "bob" {
		t.Fatalf("after remove: %q", lines)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

// ---- in-process SSH server ----------------------------------------------------

type sshServer struct {
	t      *testing.T
	ln     net.Listener
	cfg    *ssh.ServerConfig
	hostPK ssh.PublicKey
	mu     sync.Mutex
	conns  []net.Conn
}

func newSSHServer(t *testing.T, addr string, hostKey ssh.Signer) *sshServer {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(hostKey)
	s := &sshServer{t: t, ln: ln, cfg: cfg, hostPK: hostKey.PublicKey()}
	go s.accept()
	t.Cleanup(s.stop)
	return s
}

func (s *sshServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go func() {
			_, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
			if err != nil {
				return
			}
			go ssh.DiscardRequests(reqs)
			for nc := range chans {
				if nc.ChannelType() != "direct-tcpip" {
					nc.Reject(ssh.UnknownChannelType, "no")
					continue
				}
				var p struct {
					Host     string
					Port     uint32
					OrigHost string
					OrigPort uint32
				}
				_ = ssh.Unmarshal(nc.ExtraData(), &p)
				rc, err := net.Dial("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
				if err != nil {
					nc.Reject(ssh.ConnectionFailed, err.Error())
					continue
				}
				ch, creqs, err := nc.Accept()
				if err != nil {
					rc.Close()
					continue
				}
				go ssh.DiscardRequests(creqs)
				go func() {
					defer ch.Close()
					defer rc.Close()
					go func() { _, _ = io.Copy(rc, ch); rc.(*net.TCPConn).CloseWrite() }()
					_, _ = io.Copy(ch, rc)
				}()
			}
		}()
	}
}

func (s *sshServer) stop() {
	s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
	s.mu.Unlock()
}

func newSigner(t *testing.T) (ssh.Signer, string) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id")
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
	return s, keyFile
}

func TestHostKeyIsCheckedStrictly(t *testing.T) {
	t.Setenv("WFX_KNOWN_HOSTS", filepath.Join(t.TempDir(), "known_hosts"))
	hostKey, _ := newSigner(t)
	_, clientKey := newSigner(t)
	srv := newSSHServer(t, "127.0.0.1:0", hostKey)
	v := viaSpec{User: "wfx", Addr: srv.ln.Addr().String(), Key: clientKey}

	_, err := dialSSH(v, false)
	var unknown *errUnknownHost
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), "SHA256:") {
		t.Fatalf("unknown host should be refused with its fingerprint, got %v", err)
	}
	c, err := dialSSH(v, true)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = dialSSH(v, false) // now known
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	// A CHANGED host key on the same address is refused even with accept.
	srv.stop()
	other, _ := newSigner(t)
	newSSHServer(t, v.Addr, other)
	if _, err := dialSSH(v, true); err == nil || errors.As(err, &unknown) {
		t.Fatalf("changed host key must be refused, got %v", err)
	}
}

func TestForwarderThroughSSH(t *testing.T) {
	t.Setenv("WFX_KNOWN_HOSTS", filepath.Join(t.TempDir(), "known_hosts"))
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong "+r.URL.Path)
	}))
	defer app.Close()

	hostKey, _ := newSigner(t)
	_, clientKey := newSigner(t)
	srv := newSSHServer(t, "127.0.0.1:0", hostKey)
	sshAddr := srv.ln.Addr().String()
	v := viaSpec{User: "wfx", Addr: sshAddr, Key: clientKey}
	if c, err := dialSSH(v, true); err != nil {
		t.Fatal(err)
	} else {
		c.Close()
	}

	var mu sync.Mutex
	var idleEvents []time.Time
	f := &forwarder{
		dial:   func() (*ssh.Client, error) { return dialSSH(v, false) },
		target: strings.TrimPrefix(app.URL, "http://"),
		idle:   400 * time.Millisecond,
		onIdle: func(at time.Time) { mu.Lock(); idleEvents = append(idleEvents, at); mu.Unlock() },
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.serve(ln) }()
	local := "http://" + ln.Addr().String()
	get := func(p string) string {
		t.Helper()
		cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
		res, err := cl.Get(local + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if got := get("/a"); got != "pong /a" {
		t.Fatalf("got %q", got)
	}

	// The SSH server restarts; the next request reconnects on demand.
	srv.stop()
	newSSHServer(t, sshAddr, hostKey)
	if got := get("/b"); got != "pong /b" {
		t.Fatalf("after restart got %q", got)
	}

	// An open connection keeps it alive past the idle timeout.
	hold, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("closed while a connection was open: %v", err)
	default:
	}
	hold.Close()

	// With nothing open it closes after the idle timeout.
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not close when idle")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(idleEvents) < 2 {
		t.Fatalf("idle state not reported: %v", idleEvents)
	}
}

func TestTunnelLineDown(t *testing.T) {
	tempContexts(t)
	if got := tunnelLine("deemwar"); got != "deemwar · tunnel down (opens on next command)" {
		t.Fatal(got)
	}
}
