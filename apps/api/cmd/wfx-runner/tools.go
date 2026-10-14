package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// cmdTools installs named tools from the installer allowlist into a container
// image. It is how infra/Dockerfile's AGENT_TOOLS reaches the same table
// `setup` uses, so there is one list of what wfnexus knows how to install.
//
//	--system  as root: the apk packages, then the npm -g installers
//	--user    as the runtime user: the vendor curl installers (~/.local/bin)
//	--check   every named binary is on PATH and answers --version
func cmdTools(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: wfx-runner tools --system|--user|--check NAME...")
	}
	phase, names := args[0], strings.Fields(strings.Join(args[1:], " "))
	ctx := context.Background()
	entries := make([]engine.Installer, len(names))
	for i, n := range names {
		ins, ok := engine.LookupInstaller(n)
		if !ok {
			return fmt.Errorf("unknown tool %q (not in the installer allowlist)", n)
		}
		entries[i] = ins
	}
	isNPM := func(ins engine.Installer) bool { return len(ins.Command) > 0 && ins.Command[0] == "npm" }
	install := func(n string, ins engine.Installer) error {
		fmt.Println("install", n+":", ins.Instruction)
		if err := runCommand(ctx, ins.Command, true); err != nil {
			// devin's installer ends by running its interactive login, which
			// fails with no terminal AFTER the binary is in place. An image
			// build has no terminal; the login is the operator's, later.
			if _, lerr := lookPath(n); lerr == nil {
				fmt.Printf("note %s: installer exited %v after installing; log in later inside the container\n", n, err)
				return nil
			}
			return fmt.Errorf("install %s: %w", n, err)
		}
		return nil
	}
	switch phase {
	case "--system":
		var pkgs []string
		for _, ins := range entries {
			pkgs = append(pkgs, ins.Alpine...)
		}
		if len(pkgs) > 0 {
			if err := runCommand(ctx, append([]string{"apk", "add", "--no-cache"}, pkgs...), true); err != nil {
				return fmt.Errorf("apk add: %w", err)
			}
		}
		for i, ins := range entries {
			if isNPM(ins) {
				if err := install(names[i], ins); err != nil {
					return err
				}
			}
		}
	case "--user":
		for i, ins := range entries {
			if len(ins.Command) > 0 && !isNPM(ins) {
				if err := install(names[i], ins); err != nil {
					return err
				}
			}
		}
	case "--check":
		for _, n := range names {
			out, err := exec.CommandContext(ctx, n, "--version").CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s is not usable: %v (%s)", n, err, strings.TrimSpace(string(out)))
			}
			fmt.Printf("ok  %s %s\n", n, strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
		}
	default:
		return fmt.Errorf("unknown phase %q (want --system, --user or --check)", phase)
	}
	return nil
}
