package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeCarryAPI is an in-memory /api/carry: files and env per state.
type fakeCarryAPI struct {
	mu    sync.Mutex
	files map[string]string // state|path -> base64
	env   map[string]map[string]string
	calls []string
}

func newFakeCarryAPI(t *testing.T) (*fakeCarryAPI, *httptest.Server) {
	f := &fakeCarryAPI{files: map[string]string{}, env: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		q := r.URL.Query()
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		st := q.Get("state")
		if s, ok := body["state"].(string); ok {
			st = s
		}
		out := map[string]any{"version": 1}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/carry/files":
			var es []map[string]any
			for k := range f.files {
				if s, p, _ := strings.Cut(k, "|"); s == st {
					es = append(es, map[string]any{"path": p, "kind": "file", "size": 1, "mode": 0o644})
				}
			}
			out["entries"] = es
		case "GET /api/carry/file":
			out["content_base64"] = f.files[st+"|"+q.Get("path")]
		case "PUT /api/carry/file":
			f.files[st+"|"+body["path"].(string)] = body["content_base64"].(string)
		case "GET /api/carry/env":
			if q.Get("values") != "1" {
				t.Errorf("sync must ask for values explicitly")
			}
			out["env"] = f.env[st]
		case "PUT /api/carry/env":
			if f.env[st] == nil {
				f.env[st] = map[string]string{}
			}
			for k, v := range body["vars"].(map[string]any) {
				f.env[st][k] = v.(string)
			}
		case "POST /api/carry/tools":
			out["tool"] = body["tool"]
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no route"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func writeContexts(t *testing.T, ctx map[string]string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contexts.json")
	cf := contextsFile{Contexts: map[string]*wfxContext{}}
	for name, u := range ctx {
		cf.Contexts[name] = &wfxContext{URL: u}
		cf.Current = name
	}
	b, _ := json.Marshal(cf)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WFX_CONTEXTS", path)
	t.Setenv("WFX_API", "")
}

// The whole move: local folder → dev, dev → prod, prod → another local folder.
func TestSyncLocalToServerToServerToLocal(t *testing.T) {
	dev, devSrv := newFakeCarryAPI(t)
	prod, prodSrv := newFakeCarryAPI(t)
	writeContexts(t, map[string]string{"dev": devSrv.URL, "prod": prodSrv.URL})

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "files", "home", "wfx"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "files", "home", "wfx", ".gitconfig"), []byte("[user]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "env.defaults"), []byte("LOG_LEVEL=debug\n# comment\n"), 0o600)

	if err := run([]string{"sync", "--from", "local:" + src, "--to", "dev"}); err != nil {
		t.Fatalf("local → dev: %v", err)
	}
	if dev.files["|/home/wfx/.gitconfig"] == "" || dev.env[""]["LOG_LEVEL"] != "debug" {
		t.Fatalf("dev did not receive: files=%v env=%v", dev.files, dev.env)
	}
	if err := run([]string{"sync", "--from", "dev", "--to", "prod", "--to-state", "worker-1"}); err != nil {
		t.Fatalf("dev → prod: %v", err)
	}
	if prod.files["worker-1|/home/wfx/.gitconfig"] == "" || prod.env["worker-1"]["LOG_LEVEL"] != "debug" {
		t.Fatalf("prod did not receive: files=%v env=%v", prod.files, prod.env)
	}
	out := t.TempDir()
	if err := run([]string{"sync", "--from", "prod", "--state", "worker-1", "--to", "local:" + out}); err != nil {
		t.Fatalf("prod → local: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "files", "home", "wfx", ".gitconfig"))
	if err != nil || string(got) != "[user]\n" {
		t.Fatalf("local copy: %q %v", got, err)
	}
	info, _ := os.Stat(filepath.Join(out, "env.defaults"))
	// Windows has no Unix permission bits; os.Stat reports 0666 there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("env.defaults must be 0600, is %o", info.Mode().Perm())
	}
	// --dry-run writes nothing.
	before := len(prod.files)
	if err := run([]string{"sync", "--from", "local:" + src, "--to", "prod", "--state", "other", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if len(prod.files) != before {
		t.Fatal("dry run wrote")
	}
}

func TestToolAddTargetsWorkerAndContext(t *testing.T) {
	f, srv := newFakeCarryAPI(t)
	writeContexts(t, map[string]string{"prod": srv.URL})
	if err := run([]string{"tool", "add", "jq@1.7.1", "--on", "worker-1", "--context", "prod"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /api/carry/tools" {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestSyncRefusesWorkflowsInOnly(t *testing.T) {
	writeContexts(t, map[string]string{})
	err := run([]string{"sync", "--from", "local:x", "--to", "local:y", "--only", "workflows"})
	if err == nil || !strings.Contains(err.Error(), "wfx publish") {
		t.Fatalf("want a pointer to publish, got %v", err)
	}
}

func TestDeployPlans(t *testing.T) {
	cases := []struct {
		args []string
		want []string
		not  []string
	}{
		{[]string{"server", "--to", "local", "--version", "v0.2.0"},
			[]string{"sh -c", "install.sh", "WFX_BIN=wfx-server", "WFX_VERSION=v0.2.0"}, nil},
		{[]string{"runner", "--to", "ssh://ops@box:2222"},
			[]string{"ssh -p 2222 ops@box", "WFX_BIN=wfx-runner"}, nil},
		{[]string{"server", "--to", "ssh://ops@box", "--image"},
			[]string{"ssh ops@box", "docker run", "127.0.0.1:8090:8090", "-e CARRY_REPO", "-e AWS_SECRET_ACCESS_KEY", "ghcr.io/muthuishere/wfx-server:latest"},
			[]string{"AWS_SECRET_ACCESS_KEY="}},
		{[]string{"server", "--to", "k8s", "--version", "v1.0.0", "--set", "ingress.enabled=true", "-f", "my.yaml"},
			[]string{"helm upgrade --install wfnexus infra/helm/wfnexus --namespace wfnexus --create-namespace --set image.tag=v1.0.0 -f my.yaml --set ingress.enabled=true"}, nil},
		{[]string{"server", "--compose", "--version", "v0.2.0"},
			[]string{"sh -c", "git clone --depth 1 --branch v0.2.0 https://github.com/muthuishere/wfnexus.git wfnexus", "cp compose.env.example .env", "docker compose up -d --build"}, nil},
		{[]string{"server", "--compose", "--to", "ssh://ops@box", "--dir", "/srv/wfx"},
			[]string{"ssh ops@box", "git -C /srv/wfx pull --ff-only", "[ -f .env ] ||"}, nil},
	}
	for _, c := range cases {
		o, err := parseDeploy(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		steps, err := planDeploy(o)
		if err != nil {
			t.Fatal(err)
		}
		line := steps[0].String()
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("%v: plan %q lacks %q", c.args, line, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(line, n) {
				t.Errorf("%v: plan %q must not contain %q", c.args, line, n)
			}
		}
	}
	for _, bad := range [][]string{{"runner", "--compose"}, {"server", "--compose", "--to", "k8s"}, {"server", "--compose", "--image"}} {
		if _, err := parseDeploy(bad); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	if _, err := parseDeploy([]string{"server", "--to", "ftp://x"}); err == nil {
		t.Error("an unknown target must be refused")
	}
	writeContexts(t, map[string]string{})
	if err := run([]string{"profile", "create", "k", "--install", "k8s", "--dry-run"}); err != nil {
		t.Errorf("dry run: %v", err)
	}
	if err := run([]string{"worker", "install", "--to", "ssh://ops@box", "--dry-run"}); err != nil {
		t.Errorf("worker dry run: %v", err)
	}
	// The old spelling is gone, and says where it went.
	err := run([]string{"install", "server", "--to", "local"})
	if err == nil || !strings.Contains(err.Error(), "wfx profile create") {
		t.Fatalf("`wfx install server` must point at `wfx profile create`, got %v", err)
	}
}
