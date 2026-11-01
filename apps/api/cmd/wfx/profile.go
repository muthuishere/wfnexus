package main

import (
	"fmt"
	"strings"
)

// `wfx profile` is kubectl's context model for wfx servers: a named server you
// switch between. A profile is the SAME record a context always was
// (contexts.json, 0600) — `wfx context …` keeps working as an alias.
//
//	wfx profile create <name> --url U [--via ssh://user@host]     register one
//	wfx profile create <name> --install local|ssh://…|k8s          provision, register, log in
//	        [--compose|--image] [--version v] [--port p] [--dry-run] [--no-login]
//	wfx profile use|switch <name>   wfx profile ls   wfx profile current   wfx profile rm <name>
//
// Profiles are servers only. A runner is not something you talk to, so it has
// no profile: `wfx worker install --to …` provisions one for the current server.
func profileCmd(args []string) error {
	const use = "usage: wfx profile create <name> --url U [--via ssh://user@host]\n" +
		"       wfx profile create <name> --install local|ssh://user@host|k8s [--compose|--image] [--version v] [--dry-run] [--no-login]\n" +
		"       wfx profile use|switch <name> | ls | current | rm <name>"
	switch first(args) {
	case "", "ls", "list":
		return contextCmd([]string{"ls"})
	case "use", "switch":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		if err := contextCmd([]string{"use", args[1]}); err != nil {
			return err
		}
		fmt.Printf("switched to %s\n", hostKey(args[1]))
		return nil
	case "current":
		c, err := loadContexts()
		if err != nil {
			return err
		}
		if c.Current == "" {
			return fmt.Errorf("no current profile — `wfx profile create <name> --url …`")
		}
		fmt.Println(c.Current)
		return nil
	case "rm", "remove", "delete":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		return contextCmd([]string{"rm", args[1]})
	case "create", "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("%s", use)
		}
		if target := flagOf(args, "--install", ""); target != "" {
			return profileInstall(args[1], target, args[2:])
		}
		if flagOf(args, "--url", "") == "" {
			return fmt.Errorf("%s", use)
		}
		return contextAdd(args[1:])
	}
	return fmt.Errorf("%s", use)
}

// profileFromInstall is the profile a provisioned server is reached by: always
// loopback, through an ssh tunnel when it lives on another box.
func profileFromInstall(o deployOpts) (url, via string) {
	url = "http://127.0.0.1:" + o.port
	if strings.HasPrefix(o.to, "ssh://") {
		via = o.to
	}
	return url, via
}

// installServes says whether the install leaves a server RUNNING, so a login
// can follow. A bare binary is installed, not started; k8s needs a port-forward.
func installServes(o deployOpts) bool { return o.image || o.compose }

func profileInstall(name, target string, rest []string) error {
	o, err := parseDeploy(append([]string{"server", "--to", target}, rest...))
	if err != nil {
		return err
	}
	dry := hasFlag(rest, "--dry-run")
	if err := runDeploy(o, dry); err != nil {
		return err
	}
	u, via := profileFromInstall(o)
	add := []string{name, "--url", u}
	if via != "" {
		add = append(add, "--via", via)
	}
	if dry {
		fmt.Printf("would register profile %s → %s", name, u)
		if via != "" {
			fmt.Printf(" via %s", via)
		}
		fmt.Println()
		return nil
	}
	if err := contextAdd(add); err != nil {
		return err
	}
	switch {
	case o.to == "k8s":
		fmt.Printf("\nnext:\n  kubectl -n %s port-forward svc/%s %s:8090 &\n  wfx login --url %s\n", o.namespace, o.release, o.port, name)
	case !installServes(o):
		fmt.Printf("\nnext:\n  start %s on the box (no config = local mode), then: wfx login --url %s\n", o.bin(), name)
	case hasFlag(rest, "--no-login"):
		fmt.Printf("\nnext:\n  wfx login --url %s\n", name)
	default:
		fmt.Printf("\nlogging in to %s\n", name)
		if err := login([]string{"--url", name}); err != nil {
			return fmt.Errorf("the server is up and the profile is saved, but the login did not finish: %w\n  retry with: wfx login --url %s", err, name)
		}
	}
	return nil
}
