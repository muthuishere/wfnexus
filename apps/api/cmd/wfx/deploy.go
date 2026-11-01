package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// The provisioning plans behind `wfx profile create <name> --install …` (the
// server) and `wfx worker install --to …` (a runner) — get either onto a machine with what already exists, then configure it with
// `wfx tool` / `wfx config`. Nothing here is new machinery:
//
//   binary  install.sh (checksum-verified, WFX_BIN picks the binary), run
//           here or over ssh — the ship-a-gettable-binary release assets
//   --image the GHCR images built from infra/Dockerfile{,.runner}, via docker
//           run here or over ssh
//   --compose the repo's docker-compose.yml (server + Postgres), cloned into
//           --dir and brought up with `docker compose up -d --build`, here or
//           over ssh; .env starts from compose.env.example and is never
//           overwritten
//   k8s     `helm upgrade --install` of infra/helm/wfnexus (or --chart)
//
// Every plan prints before it runs; --dry-run prints only.

type deployStep struct {
	what string
	argv []string
}

func (s deployStep) String() string {
	q := make([]string, len(s.argv))
	for i, a := range s.argv {
		if strings.ContainsAny(a, " |;&$'\"") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}

type deployOpts struct {
	component string // server | runner
	to        string // local | ssh://… | k8s
	image     bool
	compose   bool
	dir       string // --compose checkout
	version   string
	repo      string
	chart     string
	namespace string
	release   string
	sets      []string
	values    []string
	name      string // container name
	port      string
}

func multiFlag(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func parseDeploy(args []string) (deployOpts, error) {
	o := deployOpts{
		component: first(args),
		to:        flagOf(args, "--to", "local"),
		image:     hasFlag(args, "--image"),
		compose:   hasFlag(args, "--compose"),
		dir:       flagOf(args, "--dir", "wfnexus"),
		version:   flagOf(args, "--version", os.Getenv("WFX_VERSION")),
		repo:      flagOf(args, "--repo", envOr("WFX_REPO", "muthuishere/wfnexus")),
		chart:     flagOf(args, "--chart", "infra/helm/wfnexus"),
		namespace: flagOf(args, "--namespace", "wfnexus"),
		sets:      multiFlag(args, "--set"),
		values:    multiFlag(args, "-f"),
		port:      flagOf(args, "--port", "8090"),
	}
	if o.component != "server" && o.component != "runner" {
		return o, fmt.Errorf("usage: wfx profile create <name> --install local|ssh://user@host|k8s [--image|--compose] | wfx worker install --to …")
	}
	if o.compose && (o.component != "server" || o.to == "k8s" || o.image) {
		return o, fmt.Errorf("--compose installs the server stack with docker compose, --to local or ssh://; not with a runner, k8s or --image")
	}
	o.release = flagOf(args, "--release", "wfnexus")
	o.name = flagOf(args, "--name", "wfx-"+o.component)
	if o.to != "local" && o.to != "k8s" && !strings.HasPrefix(o.to, "ssh://") {
		return o, fmt.Errorf("--to %q: local | ssh://user@host[:port] | k8s", o.to)
	}
	return o, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// sshArgv turns ssh://user@host:port into ssh's own argv.
func sshArgv(target string) ([]string, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("--to %q: want ssh://user@host[:port]", target)
	}
	argv := []string{"ssh"}
	if p := u.Port(); p != "" {
		argv = append(argv, "-p", p)
	}
	host := u.Hostname()
	if u.User != nil && u.User.Username() != "" {
		host = u.User.Username() + "@" + host
	}
	return append(argv, host), nil
}

func (o deployOpts) bin() string { return "wfx-" + o.component }

func (o deployOpts) imageRef() string {
	tag := o.version
	if tag == "" {
		tag = "latest"
	}
	return "ghcr.io/" + strings.Split(o.repo, "/")[0] + "/" + o.bin() + ":" + tag
}

// installScript is the install.sh line, with WFX_BIN choosing the binary.
func (o deployOpts) installScript() string {
	env := "WFX_BIN=" + o.bin() + " WFX_REPO=" + o.repo
	if o.version != "" {
		env += " WFX_VERSION=" + o.version
	}
	return "curl -fsSL https://raw.githubusercontent.com/" + o.repo + "/main/install.sh | " + env + " sh"
}

// dockerRun is the container line. Configuration is passed BY NAME (-e NAME),
// so a value in this shell's env reaches the container without being printed.
func (o deployOpts) dockerRun() string {
	parts := []string{"docker", "run", "-d", "--name", o.name, "--restart", "unless-stopped"}
	if o.component == "server" {
		// Loopback only: reach it with `wfx profile create … --via ssh://`.
		parts = append(parts, "-p", "127.0.0.1:"+o.port+":8090", "-v", o.name+"-data:/home/wfx")
	} else {
		parts = append(parts, "-e", "WFX_URL", "-e", "WFX_TOKEN", "-e", "WFX_NAME", "-e", "WFX_LABELS")
	}
	for _, n := range []string{"CARRY_REPO", "CARRY_S3_ENDPOINT", "CARRY_KEY_FILE", "CARRY_STATE", "CARRY_TOOL_PUBKEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		parts = append(parts, "-e", n)
	}
	return strings.Join(append(parts, o.imageRef()), " ")
}

// composeUp is the compose line: fetch the repo at --version (or pull the
// existing checkout), keep any .env already there, and bring the stack up.
func (o deployOpts) composeUp() string {
	clone := "git clone --depth 1"
	if o.version != "" {
		clone += " --branch " + o.version
	}
	clone += " https://github.com/" + o.repo + ".git " + o.dir
	return "{ [ -d " + o.dir + "/.git ] && git -C " + o.dir + " pull --ff-only || " + clone + "; }" +
		" && cd " + o.dir +
		" && { [ -f .env ] || cp compose.env.example .env; }" +
		" && docker compose up -d --build"
}

func planDeploy(o deployOpts) ([]deployStep, error) {
	switch {
	case o.compose && o.to == "local":
		return []deployStep{{"bring up the server stack with docker compose", []string{"sh", "-c", o.composeUp()}}}, nil
	case o.compose:
		ssh, err := sshArgv(o.to)
		if err != nil {
			return nil, err
		}
		return []deployStep{{"bring up the server stack with docker compose on " + o.to, append(ssh, o.composeUp())}}, nil
	case o.to == "k8s":
		argv := []string{"helm", "upgrade", "--install", o.release, o.chart, "--namespace", o.namespace, "--create-namespace"}
		if o.version != "" {
			argv = append(argv, "--set", "image.tag="+o.version)
		}
		for _, f := range o.values {
			argv = append(argv, "-f", f)
		}
		for _, s := range o.sets {
			argv = append(argv, "--set", s)
		}
		return []deployStep{{"install the chart (" + o.component + ")", argv}}, nil
	case o.to == "local":
		if o.image {
			return []deployStep{{"start the " + o.component + " container", []string{"sh", "-c", o.dockerRun()}}}, nil
		}
		return []deployStep{{"download and verify " + o.bin(), []string{"sh", "-c", o.installScript()}}}, nil
	default:
		ssh, err := sshArgv(o.to)
		if err != nil {
			return nil, err
		}
		if o.image {
			return []deployStep{{"start the " + o.component + " container on " + o.to, append(ssh, o.dockerRun())}}, nil
		}
		return []deployStep{{"download and verify " + o.bin() + " on " + o.to, append(ssh, o.installScript())}}, nil
	}
}

// nextSteps is what to type after the install: reach it, then configure it.
func (o deployOpts) nextSteps() []string {
	var out []string
	switch {
	case o.component == "runner":
		out = append(out, "wfx workers                       # the join line for this runner, then: wfx-runner join …")
	case o.to == "k8s":
		out = append(out, "kubectl -n "+o.namespace+" port-forward svc/"+o.release+" "+o.port+":8090 &",
			"wfx login --url http://127.0.0.1:"+o.port)
	case strings.HasPrefix(o.to, "ssh://"):
		out = append(out, "wfx profile create <name> --url http://127.0.0.1:"+o.port+" --via "+o.to)
	case o.compose:
		out = append(out, "wfx login --url http://127.0.0.1:"+o.port+"   # settings: "+o.dir+"/.env, then docker compose up -d")
	default:
		if !o.image {
			out = append(out, o.bin()+" &                     # start it (no config = local mode: SQLite + a folder)")
		}
	}
	return append(out,
		"wfx config status                 # is carry on for this server",
		"wfx tool add jq@1.7.1             # tools; wfx skill add / wfx mcp add for the rest")
}

func deployCmd(args []string) error {
	o, err := parseDeploy(args)
	if err != nil {
		return err
	}
	if err := runDeploy(o, hasFlag(args, "--dry-run")); err != nil {
		return err
	}
	fmt.Println("\nnext:")
	for _, n := range o.nextSteps() {
		fmt.Println("  " + n)
	}
	return nil
}

// runDeploy prints every step of the plan and runs it unless dry.
func runDeploy(o deployOpts, dry bool) error {
	steps, err := planDeploy(o)
	if err != nil {
		return err
	}
	for _, s := range steps {
		fmt.Printf("→ %s\n  %s\n", s.what, s)
		if dry {
			continue
		}
		if _, err := exec.LookPath(s.argv[0]); err != nil {
			return fmt.Errorf("%s is not installed here — needed to %s", s.argv[0], s.what)
		}
		cmd := exec.Command(s.argv[0], s.argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", s.what, err)
		}
	}
	if dry {
		fmt.Println("(dry run — nothing was run)")
	}
	return nil
}
