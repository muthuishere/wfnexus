package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// `wfx server deploy|status|rollback <host>`: the one-line, no-sudo upgrade
// of a bare host (#115, docs/proposals/115-deploy-options.md, option 1).
//
// The laptop resolves and verifies the release, then over plain ssh:
//
//	~/.local/share/wfnexus/releases/<v>/wfx-server   one dir per release (last 3 kept)
//	~/.local/share/wfnexus/current -> releases/<v>   what the unit runs
//	~/.local/share/wfnexus/backups/                  sqlite .backup before every swap (last 5)
//	~/.config/systemd/user/wfnexus.service           systemd --user, needs linger
//
// A swap is backup → relink → restart → health + version + model probe; any
// failure relinks the previous release. Root is never used: a host still on
// the old system unit gets the one-time command printed, never run.

// remoteRunner runs one shell command on the target and returns its stdout.
// The ssh one is real; tests swap in one that runs the command locally.
type remoteRunner interface {
	run(cmd string, stdin io.Reader) (string, error)
}

type sshRunner struct{ argv []string } // ssh [-p N] host

func (s sshRunner) run(cmd string, stdin io.Reader) (string, error) {
	c := exec.Command(s.argv[0], append(append([]string{}, s.argv[1:]...), cmd)...)
	var out bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = stdin, &out, os.Stderr
	err := c.Run()
	return out.String(), err
}

// sshFor accepts an ~/.ssh/config alias (wfnexus-db), user@host, or ssh://user@host:port.
func sshFor(host string) (remoteRunner, error) {
	if strings.HasPrefix(host, "ssh://") {
		argv, err := sshArgv(host)
		if err != nil {
			return nil, err
		}
		return sshRunner{argv}, nil
	}
	if host == "" || strings.HasPrefix(host, "-") {
		return nil, fmt.Errorf("want a host: an ssh alias, user@host or ssh://user@host:port")
	}
	return sshRunner{[]string{"ssh", host}}, nil
}

// deployScript is the whole on-host half. POSIX sh, one entry point per verb,
// sent on stdin (`sh -s -- <verb> …`) so nothing needs installing on the host.
const deployScript = `set -eu
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
B="$HOME/.local/share/wfnexus"; R="$B/releases"
U="$HOME/.config/systemd/user/wfnexus.service"
PORT="${WFX_DEPLOY_PORT:-8090}"
cur() { [ -L "$B/current" ] && basename "$(readlink "$B/current")" || true; }
restart() { systemctl --user daemon-reload; systemctl --user enable wfnexus.service >/dev/null 2>&1 || true; systemctl --user restart wfnexus.service; }

unit() {
  mkdir -p "$(dirname "$U")"
  cat > "$U.new" <<'UNIT'
[Unit]
Description=wfnexus workflow server (deployed by wfx server deploy)
After=network-online.target

[Service]
WorkingDirectory=%h
Environment=WFX_CONFIG=%h/.config/wfx/config.yaml
Environment=PATH=%h/.local/bin:%h/.npm-global/bin:%h/.opencode/bin:/usr/local/bin:/usr/bin:/bin
EnvironmentFile=-%h/.config/wfx/env
EnvironmentFile=-%h/.config/apl/master.env
ExecStart=%h/.local/share/wfnexus/current/wfx-server
Restart=always
RestartSec=5
# a co-located production Postgres always wins; wfx server status checks these are enforced
MemoryMax=32G
CPUQuota=600%
Nice=10
IOWeight=50
TasksMax=4096
NoNewPrivileges=true

[Install]
WantedBy=default.target
UNIT
  mv "$U.new" "$U"
}

limits() {
  id=$(id -u)
  ctl=$(cat "${WFX_CGROUP_ROOT:-/sys/fs/cgroup}/user.slice/user-$id.slice/user@$id.service/cgroup.controllers" 2>/dev/null || true)
  echo "controllers=$ctl"
  echo "memorymax=$(systemctl --user show wfnexus.service -p MemoryMax --value 2>/dev/null || true)"
  echo "cpuquota=$(systemctl --user show wfnexus.service -p CPUQuotaPerSecUSec --value 2>/dev/null || true)"
}

verb="$1"; shift
case "$verb" in
probe)
  echo "arch=$(uname -m)"
  echo "systemunit=$(systemctl is-active wfnexus.service 2>/dev/null || true)"
  echo "linger=$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || true)"
  echo "current=$(cur)"
  ;;
receive)
  mkdir -p "$R/$1"
  cat > "$R/$1/wfx-server.part"
  chmod 755 "$R/$1/wfx-server.part"
  mv "$R/$1/wfx-server.part" "$R/$1/wfx-server"
  touch "$R/$1"
  ;;
swap)
  if [ "$(systemctl is-active wfnexus.service 2>/dev/null || true)" = active ]; then
    echo "the system unit wfnexus.service is still running" >&2; exit 4
  fi
  echo "prev=$(cur)"
  if [ -f "$B/wfnexus.db" ]; then
    mkdir -p "$B/backups"
    f="$B/backups/wfnexus-$(date +%Y%m%d-%H%M%S)-pre-$1.db"
    sqlite3 "$B/wfnexus.db" ".backup '$f'"
    echo "backup=$f"
    ls -1t "$B/backups"/wfnexus-*.db 2>/dev/null | tail -n +6 | while read -r old; do rm -f "$old"; done
  fi
  unit
  ln -sfn "releases/$1" "$B/current"
  restart
  ;;
check)
  i=0
  until curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; do
    i=$((i+1)); [ "$i" -ge "${WFX_DEPLOY_WAIT:-60}" ] && { echo "health: no 200 from /api/health in ${WFX_DEPLOY_WAIT:-60}s" >&2; exit 5; }
    sleep 1
  done
  echo "version=$("$B/current/wfx-server" --version 2>/dev/null | head -n1)"
  if [ "${1:-}" != skip-model ]; then
    echo "doctor=$(curl -fsS --max-time 90 "http://127.0.0.1:$PORT/api/doctor?probe=1" 2>/dev/null | tr -d '\n' || true)"
  fi
  limits
  ;;
revert)
  [ -n "$1" ] && [ -d "$R/$1" ] || { echo "no release $1 to go back to" >&2; exit 6; }
  ln -sfn "releases/$1" "$B/current"
  restart
  echo "current=$1"
  ;;
previous)
  c=$(cur); seen=
  for d in $(ls -1t "$R" 2>/dev/null); do
    if [ -n "$seen" ]; then echo "prev=$d"; exit 0; fi
    [ "$d" = "$c" ] && seen=1
  done
  ;;
prune)
  c=$(cur); n=0
  for d in $(ls -1t "$R" 2>/dev/null); do
    n=$((n+1))
    [ "$n" -gt 3 ] && [ "$d" != "$c" ] && rm -rf "${R:?}/$d"
  done
  ls -1t "$R"
  ;;
status)
  echo "current=$(cur)"
  echo "releases=$(ls -1t "$R" 2>/dev/null | tr '\n' ' ')"
  echo "active=$(systemctl --user is-active wfnexus.service 2>/dev/null || true)"
  echo "systemunit=$(systemctl is-active wfnexus.service 2>/dev/null || true)"
  echo "since=$(systemctl --user show wfnexus.service -p ActiveEnterTimestamp --value 2>/dev/null || true)"
  echo "health=$(curl -fsS --max-time 5 "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1 && echo ok || echo down)"
  [ -x "$B/current/wfx-server" ] && echo "version=$("$B/current/wfx-server" --version 2>/dev/null | head -n1)"
  limits
  ;;
*) echo "unknown verb $verb" >&2; exit 2 ;;
esac
`

func remote(r remoteRunner, stdin io.Reader, verb string, args ...string) (map[string]string, error) {
	cmd := "sh -s -- " + verb
	for _, a := range args {
		cmd += " " + shQuote(a)
	}
	script := deployScript
	var in io.Reader = strings.NewReader(script)
	if stdin != nil {
		// The script is the first argument to sh -c; the payload stays on stdin.
		cmd = "sh -c " + shQuote(script) + " deploy " + verb
		for _, a := range args {
			cmd += " " + shQuote(a)
		}
		in = stdin
	}
	out, err := r.run(cmd, in)
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = v
		}
	}
	if err != nil {
		return kv, fmt.Errorf("%s on the host: %w", verb, err)
	}
	return kv, nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fetchRelease is swapped in tests; the real one asks GitHub.
var fetchRelease = githubRelease

// githubRelease returns the tag (latest when version is "") and the verified
// wfx-server-linux-<arch> binary. GITHUB_TOKEN is used by name, never printed.
func githubRelease(repo, version, arch string) (string, []byte, error) {
	get := func(u string) ([]byte, error) {
		req, _ := http.NewRequest("GET", u, nil)
		if t := os.Getenv("GITHUB_TOKEN"); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}
	if version == "" {
		b, err := get("https://api.github.com/repos/" + repo + "/releases/latest")
		if err != nil {
			return "", nil, err
		}
		var rel struct {
			Tag string `json:"tag_name"`
		}
		if err := json.Unmarshal(b, &rel); err != nil || rel.Tag == "" {
			return "", nil, fmt.Errorf("latest release of %s: no tag", repo)
		}
		version = rel.Tag
	}
	asset := "wfx-server-linux-" + arch
	base := "https://github.com/" + repo + "/releases/download/" + version + "/"
	fmt.Printf("→ download %s %s\n", asset, version)
	bin, err := get(base + asset)
	if err != nil {
		return "", nil, err
	}
	sums, err := get(base + "checksums.txt")
	if err != nil {
		return "", nil, err
	}
	if err := verifySum(sums, asset, bin); err != nil {
		return "", nil, err
	}
	return version, bin, nil
}

func verifySum(sums []byte, asset string, bin []byte) error {
	h := sha256.Sum256(bin)
	got := hex.EncodeToString(h[:])
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			if f[0] != got {
				return fmt.Errorf("%s: checksum mismatch (want %s, got %s)", asset, f[0], got)
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not in checksums.txt", asset)
}

func goArch(uname string) (string, error) {
	switch uname {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported host architecture %q", uname)
}

// oneTimeRoot is what a host still on the old system unit needs, once. Printed, never run.
func oneTimeRoot(host string) error {
	return fmt.Errorf(`%s still runs the old SYSTEM unit wfnexus.service. One time, as root, run:

    ssh <a root login for %s> systemctl disable --now wfnexus.service

and if linger is off for this user:  loginctl enable-linger <user>
Then re-run: wfx server deploy %s   (wfx never runs root commands itself)`, host, host, host)
}

type serverDeployOpts struct {
	host, version, binary, repo string
	skipModel, skipCLIs         bool
}

// coreCLIs installs the CLIs the core modules wrap (apl, gh, aos,
// ctx-optimize) into ~/.local/bin on the host — idempotent, no sudo.
//
//go:embed core-clis.sh
var coreCLIs string

func serverCmd(args []string) error {
	verb := first(args)
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return fmt.Errorf("usage: wfx server deploy|status|rollback <host> [--version vX | --binary ./wfx-server] [--skip-model] [--skip-clis]")
	}
	r, err := sshFor(args[1])
	if err != nil {
		return err
	}
	rest := args[2:]
	o := serverDeployOpts{
		host:      args[1],
		version:   flagOf(rest, "--version", ""),
		binary:    flagOf(rest, "--binary", ""),
		repo:      flagOf(rest, "--repo", envOr("WFX_REPO", "muthuishere/wfnexus")),
		skipModel: hasFlag(rest, "--skip-model"),
		skipCLIs:  hasFlag(rest, "--skip-clis"),
	}
	switch verb {
	case "deploy":
		return serverDeploy(r, o)
	case "status":
		return serverStatus(r, o.host)
	case "rollback":
		return serverRollback(r, o)
	}
	return fmt.Errorf("wfx server %q: deploy | status | rollback", verb)
}

func serverDeploy(r remoteRunner, o serverDeployOpts) error {
	p, err := remote(r, nil, "probe")
	if err != nil {
		return err
	}
	if p["systemunit"] == "active" {
		return oneTimeRoot(o.host)
	}
	if p["linger"] == "no" {
		fmt.Printf("⚠ linger is OFF for this user on %s: the server stops at logout and does not start at boot. One time, as root: loginctl enable-linger <user>\n", o.host)
	}
	arch, err := goArch(p["arch"])
	if err != nil {
		return err
	}

	version, bin := o.version, []byte(nil)
	if o.binary != "" {
		if bin, err = os.ReadFile(o.binary); err != nil {
			return err
		}
		if version == "" {
			version = "local-" + time.Now().UTC().Format("20060102-150405")
		}
	} else if version, bin, err = fetchRelease(o.repo, o.version, arch); err != nil {
		return err
	}
	if version == p["current"] {
		fmt.Printf("%s already runs %s — re-deploying it\n", o.host, version)
	}

	if !o.skipCLIs && os.Getenv("WFX_DEPLOY_SKIP_CLIS") != "1" {
		// Core module CLIs are part of every server. A failure is reported,
		// never fatal: the server runs without them, the Modules page says so.
		fmt.Println("→ external module CLI (apl); aos, ctx-optimize, jevx, tmvp, gh are built in")
		out, err := r.run("sh -s --", strings.NewReader(coreCLIs))
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l != "" {
				fmt.Println("  " + l)
			}
		}
		if err != nil {
			fmt.Printf("  ! not all installed (%v) — install from the Modules page\n", err)
		}
	}
	fmt.Printf("→ upload %s to %s (%d MB)\n", version, o.host, len(bin)>>20)
	if _, err := remote(r, bytes.NewReader(bin), "receive", version); err != nil {
		return err
	}
	fmt.Println("→ back up the DB, switch current, restart")
	sw, err := remote(r, nil, "swap", version)
	if b := sw["backup"]; b != "" {
		fmt.Println("  backup " + b)
	}
	if err != nil {
		return err
	}
	prev := sw["prev"]

	fmt.Println("→ health check")
	if bad := checkDeployed(r, version, o.binary == "", o.skipModel); bad != "" {
		fmt.Printf("✗ %s\n", bad)
		if prev == "" || prev == version {
			return fmt.Errorf("deploy of %s failed and there is no previous release to roll back to: %s", version, bad)
		}
		fmt.Printf("→ roll back to %s\n", prev)
		if _, err := remote(r, nil, "revert", prev); err != nil {
			return fmt.Errorf("deploy failed (%s) AND the rollback to %s failed: %w", bad, prev, err)
		}
		if again := checkDeployed(r, prev, false, true); again != "" {
			return fmt.Errorf("deploy failed (%s); rolled back to %s but it is not healthy either: %s", bad, prev, again)
		}
		return fmt.Errorf("deploy of %s failed (%s); rolled back to %s, which is healthy", version, bad, prev)
	}
	if _, err := remote(r, nil, "prune"); err != nil {
		fmt.Printf("⚠ could not prune old releases: %v\n", err)
	}
	fmt.Printf("✓ %s runs %s", o.host, version)
	if prev != "" && prev != version {
		fmt.Printf(" (was %s — wfx server rollback %s)", prev, o.host)
	}
	fmt.Println()
	return nil
}

// checkDeployed returns "" when healthy, or what is wrong.
func checkDeployed(r remoteRunner, version string, wantVersion, skipModel bool) string {
	arg := ""
	if skipModel {
		arg = "skip-model"
	}
	c, err := remote(r, nil, "check", arg)
	if err != nil {
		return err.Error()
	}
	if wantVersion && !strings.Contains(c["version"], strings.TrimPrefix(version, "v")) {
		return fmt.Sprintf("the binary reports %q, not %s", c["version"], version)
	}
	if !skipModel {
		if bad := modelNotReady(c["doctor"]); bad != "" {
			return bad
		}
	}
	warnLimits(c)
	return ""
}

func modelNotReady(doctor string) string {
	if doctor == "" {
		return "model: /api/doctor did not answer (use --skip-model to deploy anyway)"
	}
	var d struct {
		Probe *struct {
			Provider, Model, Error string
			OK                     bool
		} `json:"probe"`
		Result *struct {
			Probe *struct {
				Provider, Model, Error string
				OK                     bool
			} `json:"probe"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(doctor), &d); err != nil {
		return "model: unreadable /api/doctor answer"
	}
	p := d.Probe
	if p == nil && d.Result != nil {
		p = d.Result.Probe
	}
	if p == nil {
		return "model: /api/doctor ran no probe"
	}
	if !p.OK {
		return fmt.Sprintf("model not ready: %s/%s: %s", p.Provider, p.Model, p.Error)
	}
	return ""
}

// warnLimits says, loudly, when the unit's memory/CPU caps are not enforced
// because systemd has not delegated those controllers to the user manager.
func warnLimits(kv map[string]string) bool {
	ctl := " " + kv["controllers"] + " "
	var missing []string
	for _, c := range []string{"memory", "cpu"} {
		if !strings.Contains(ctl, " "+c+" ") {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return true
	}
	fmt.Printf(`
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
!! MemoryMax / CPUQuota are NOT ENFORCED: the user manager lacks the
!! %s cgroup controller(s) (have: %q). A co-located Postgres is
!! unprotected. One time, as root, delegate them:
!!   mkdir -p /etc/systemd/system/user@.service.d
!!   printf '[Service]\nDelegate=cpu cpuset io memory pids\n' > /etc/systemd/system/user@.service.d/delegate.conf
!!   systemctl daemon-reload   (then re-login / restart user@<uid>.service)
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
`, strings.Join(missing, "+"), strings.TrimSpace(kv["controllers"]))
	return false
}

func serverStatus(r remoteRunner, host string) error {
	s, err := remote(r, nil, "status")
	if err != nil {
		return err
	}
	fmt.Printf("host       %s\n", host)
	fmt.Printf("version    %s  (current → %s)\n", or(s["version"], "-"), or(s["current"], "none"))
	fmt.Printf("service    %s since %s\n", or(s["active"], "-"), or(s["since"], "-"))
	fmt.Printf("health     %s\n", s["health"])
	fmt.Printf("releases   %s\n", strings.TrimSpace(s["releases"]))
	fmt.Printf("limits     MemoryMax=%s CPUQuota=%s/s  controllers: %s\n", s["memorymax"], s["cpuquota"], s["controllers"])
	if s["systemunit"] == "active" {
		fmt.Println("\n⚠ the old SYSTEM unit wfnexus.service is still active: wfx server deploy will print the one-time root step")
	}
	if s["current"] != "" && !warnLimits(s) {
		return fmt.Errorf("resource limits are not enforced on %s", host)
	}
	return nil
}

func serverRollback(r remoteRunner, o serverDeployOpts) error {
	target := o.version
	if target == "" {
		p, err := remote(r, nil, "previous")
		if err != nil {
			return err
		}
		target = p["prev"]
	}
	if target == "" {
		return fmt.Errorf("no earlier release kept on %s", o.host)
	}
	fmt.Printf("→ roll back %s to %s\n", o.host, target)
	if _, err := remote(r, nil, "revert", target); err != nil {
		return err
	}
	if bad := checkDeployed(r, target, false, o.skipModel); bad != "" {
		return fmt.Errorf("rolled back to %s but it is not healthy: %s", target, bad)
	}
	fmt.Printf("✓ %s runs %s\n", o.host, target)
	return nil
}
