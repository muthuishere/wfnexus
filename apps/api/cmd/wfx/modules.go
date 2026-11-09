package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// `wfx module` — modules (ADR 0028): a task + module.yaml, installed from
// the existing registry (a skill source: git import, pin, Sync).
//
//	wfx module ls
//	wfx module install <git-url> [--ref tag|sha|branch] [--name n]   (= wfx skills import --path .)
//	wfx module sync <source>                                          (= wfx skills refresh)
//	wfx module config <module>      prompts once for each missing env var / secret
//	wfx module start <module>.<service> [--profile p | --account a] [--runs-on label] [--input k=v ...]
//	                                one supervised instance per service+account (#103)
//	wfx module stop <module>.<service> --account a
//	wfx module call <module>.<command> [--input k=v ...] [--wait]     run a command now (#109)
//	wfx module profile ls <module>
//	wfx module profile set <module> <name> --account <handle> [--input k=v ...] [--default]
//	wfx module watch ls|add|rm <module> --account a ['<json>' | <name>]
//	                                watches the running service reads every tick

type moduleRow struct {
	Name     string                     `json:"name"`
	Version  string                     `json:"version"`
	Source   string                     `json:"source"`
	Steps    []json.RawMessage          `json:"steps"`
	Commands map[string]json.RawMessage `json:"commands"`
	Svcs     map[string]json.RawMessage `json:"services"`
	Requires struct {
		Env     []string `json:"env"`
		Secrets []string `json:"secrets"`
	} `json:"requires"`
	Binary *struct {
		Carry string `json:"carry"`
	} `json:"binary"`
	Missing  []string     `json:"missing"`
	Ready    bool         `json:"ready"`
	Services []serviceRow `json:"instances"`
}

type serviceRow struct {
	Module        string     `json:"module"`
	Account       string     `json:"account"`
	RunsOn        string     `json:"runsOn"`
	State         string     `json:"state"`
	Restarts      int        `json:"restarts"`
	LastError     string     `json:"lastError"`
	LastHeartbeat *time.Time `json:"lastHeartbeat"`
}

type moduleList struct {
	Modules []moduleRow `json:"modules"`
	Errors  []string    `json:"errors"`
}

func moduleCmd(args []string) error {
	const use = "usage: wfx module ls | install <git-url> [--ref r] [--name n] | sync <source> | config <module>" +
		" | start <module>.<service> [--profile p] [--account a] [--runs-on label] [--input k=v] | stop <module>.<service> --account a" +
		" | update <aos|ctx-optimize|jevx> [--version vX] | call <module>.<command> [--input k=v] [--wait] | profile ls <module> | profile set <module> <name> --account a [--input k=v] [--default]"
	if len(args) == 0 || args[0] == "ls" || args[0] == "list" {
		return moduleLs()
	}
	switch args[0] {
	case "update":
		// Built-in tools (aos, ctx-optimize, jevx): latest, or --version for a
		// specific one (an older one is a rollback). Every runner follows.
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx module update aos|ctx-optimize|jevx [--version vX]")
		}
		var out struct {
			Name, Version string
		}
		if err := call("POST", "/api/builtins/"+args[1]+"/update", map[string]string{"version": flagOf(args, "--version", "")}, &out); err != nil {
			return err
		}
		fmt.Printf("%s -> %s (verified on the server; runners switch at their next heartbeat)\n", out.Name, out.Version)
		return nil
	case "install", "add":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		rest := append([]string{"import"}, args[1:]...)
		if flagOf(args, "--path", "") == "" {
			rest = append(rest, "--path", ".")
		}
		if err := skillsCmd(rest); err != nil {
			return err
		}
		fmt.Println("\nnext: wfx module config <module> for anything listed as missing")
		return moduleLs()
	case "sync", "refresh":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		return skillsCmd([]string{"refresh", args[1]})
	case "config", "configure":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		return moduleConfig(args[1])
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		return moduleStart(args[1], args[2:])
	case "call", "run":
		if len(args) < 2 {
			return fmt.Errorf("%s", use)
		}
		return moduleCall(args[1], args[2:])
	case "profile", "profiles":
		return moduleProfile(args[1:])
	case "watch", "watches":
		return moduleWatch(args[1:])
	case "stop":
		acct := flagOf(args, "--account", "")
		if len(args) < 2 || acct == "" {
			return fmt.Errorf("%s", use)
		}
		if err := call("DELETE", "/api/modules/"+url.PathEscape(args[1])+"/services/"+url.PathEscape(acct), nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s/%s stopped\n", args[1], acct)
		return nil
	}
	return fmt.Errorf("%s", use)
}

func moduleLs() error {
	var out moduleList
	if err := call("GET", "/api/modules", nil, &out); err != nil {
		return err
	}
	if len(out.Modules) == 0 {
		fmt.Println("no modules — wfx module install <git-url>")
	} else {
		fmt.Printf("%-24s %-10s %-16s %s\n", "MODULE", "VERSION", "SOURCE", "STATUS")
		for _, m := range out.Modules {
			st := "ready"
			if !m.Ready {
				st = "needs " + strings.Join(m.Missing, ", ")
			}
			fmt.Printf("%-24s %-10s %-16s %s\n", m.Name, m.Version, m.Source, st)
			var cmds, svcs []string
			if len(m.Steps) > 0 {
				cmds = append(cmds, m.Name)
			}
			for c := range m.Commands {
				cmds = append(cmds, m.Name+"."+c)
			}
			for s := range m.Svcs {
				svcs = append(svcs, m.Name+"."+s)
			}
			sort.Strings(cmds)
			sort.Strings(svcs)
			if len(cmds) > 0 {
				fmt.Printf("  commands: %s\n", strings.Join(cmds, ", "))
			}
			if len(svcs) > 0 {
				fmt.Printf("  services: %s\n", strings.Join(svcs, ", "))
			}
			for _, sv := range m.Services {
				fmt.Println(serviceLine(sv))
			}
		}
	}
	for _, e := range out.Errors {
		fmt.Printf("skipped: %s\n", e)
	}
	return nil
}

// moduleConfig prompts (echo off) for every missing requirement and stores it
// in the platform env — secrets encrypted, env plain. Values never touch argv.
func moduleConfig(name string) error {
	var out moduleList
	if err := call("GET", "/api/modules", nil, &out); err != nil {
		return err
	}
	for _, m := range out.Modules {
		if m.Name != name {
			continue
		}
		if len(m.Missing) == 0 {
			fmt.Printf("%s is configured\n", name)
			return nil
		}
		secret := map[string]bool{}
		for _, s := range m.Requires.Secrets {
			secret[s] = true
		}
		for _, k := range m.Missing {
			a := []string{k}
			if !secret[k] {
				a = append(a, "--plain")
			}
			if err := envSet("/api/env", a); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("no module %q — wfx module ls", name)
}

// moduleStart records one supervised instance (#103): the server runs it on a
// label it serves itself, otherwise the one worker holding --runs-on.
func moduleStart(name string, args []string) error {
	in := map[string]string{}
	for _, kv := range flagsOf(args, "--input") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--input wants k=v, got %q", kv)
		}
		in[k] = v
	}
	body := map[string]any{"account": flagOf(args, "--account", ""), "profile": flagOf(args, "--profile", ""),
		"runsOn": flagOf(args, "--runs-on", ""), "inputs": in}
	var got struct {
		Account string `json:"account"`
		RunsOn  string `json:"runsOn"`
	}
	if err := call("POST", "/api/modules/"+url.PathEscape(name)+"/services", body, &got); err != nil {
		return err
	}
	where := got.RunsOn
	if where == "" {
		where = "the server"
	}
	fmt.Printf("%s/%s will run on %s — watch it with: wfx module ls\n", name, got.Account, where)
	return nil
}

// moduleWatch registers watches for one running service through wfnexus; the
// service picks them up on its next tick.
func moduleWatch(args []string) error {
	const use = "usage: wfx module watch ls|add|rm <module> --account a ['<watch json>' | <watch name>]"
	acct := flagOf(args, "--account", "")
	var pos []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--account" {
			i++
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) < 2 || acct == "" {
		return fmt.Errorf("%s", use)
	}
	base := "/api/modules/" + url.PathEscape(pos[1]) + "/services/" + url.PathEscape(acct) + "/watches"
	switch pos[0] {
	case "ls", "list":
		var out struct {
			Watches []json.RawMessage `json:"watches"`
		}
		if err := call("GET", base, nil, &out); err != nil {
			return err
		}
		if len(out.Watches) == 0 {
			fmt.Println("no watches")
		}
		for _, w := range out.Watches {
			fmt.Println(string(w))
		}
		return nil
	case "add", "set":
		if len(pos) < 3 {
			return fmt.Errorf("%s", use)
		}
		var w map[string]any
		if err := json.Unmarshal([]byte(pos[2]), &w); err != nil {
			return fmt.Errorf("the watch must be a JSON object: %w", err)
		}
		if err := call("POST", base, w, nil); err != nil {
			return err
		}
		fmt.Printf("watch %v registered; the service reads it on its next tick\n", w["name"])
		return nil
	case "rm", "remove":
		if len(pos) < 3 {
			return fmt.Errorf("%s", use)
		}
		if err := call("DELETE", base+"/"+url.PathEscape(pos[2]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("watch %s removed\n", pos[2])
		return nil
	}
	return fmt.Errorf("%s", use)
}

func serviceLine(sv serviceRow) string {
	beat := "never"
	if sv.LastHeartbeat != nil {
		beat = time.Since(*sv.LastHeartbeat).Round(time.Second).String() + " ago"
	}
	where := sv.RunsOn
	if where == "" {
		where = "server"
	}
	line := fmt.Sprintf("  └ %-14s %-20s %-8s on %-12s heartbeat %-10s restarts %d", sv.Module, sv.Account, sv.State, where, beat, sv.Restarts)
	if sv.LastError != "" && sv.State != "running" {
		line += "  last error: " + sv.LastError
	}
	return line
}

func inputsOf(args []string) (map[string]string, error) {
	in := map[string]string{}
	for _, kv := range flagsOf(args, "--input") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--input wants k=v, got %q", kv)
		}
		in[k] = v
	}
	return in, nil
}

// moduleCall runs <module>.<command> now, as a run of its own (#109).
func moduleCall(name string, args []string) error {
	in, err := inputsOf(args)
	if err != nil {
		return err
	}
	wait := false
	for _, a := range args {
		if a == "--wait" {
			wait = true
		}
	}
	var out struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"run"`
		Output json.RawMessage `json:"output"`
	}
	if err := call("POST", "/api/modules/"+url.PathEscape(name)+"/call", map[string]any{"inputs": in, "wait": wait}, &out); err != nil {
		return err
	}
	fmt.Printf("run %s %s\n", out.Run.ID, out.Run.Status)
	if out.Run.Error != "" {
		fmt.Println("error:", out.Run.Error)
	}
	if len(out.Output) > 0 && string(out.Output) != "null" {
		fmt.Println(string(out.Output))
	}
	if !wait {
		fmt.Println("follow it: wfx runs show " + out.Run.ID)
	}
	return nil
}

// moduleProfile lists or sets a module's profiles: a handle (the account) plus
// input defaults, one of them the default — set once, globally.
func moduleProfile(args []string) error {
	const use = "usage: wfx module profile ls <module> | set <module> <name> --account <handle> [--input k=v] [--default]"
	if len(args) < 2 {
		return fmt.Errorf("%s", use)
	}
	switch args[0] {
	case "ls", "list":
		var out struct {
			Profiles []struct {
				Name    string            `json:"name"`
				Account string            `json:"account"`
				Inputs  map[string]string `json:"inputs"`
				Default bool              `json:"default"`
			} `json:"profiles"`
		}
		if err := call("GET", "/api/modules/"+url.PathEscape(args[1])+"/profiles", nil, &out); err != nil {
			return err
		}
		if len(out.Profiles) == 0 {
			fmt.Printf("no profiles — wfx module profile set %s <name> --account <handle> --default\n", args[1])
		}
		for _, p := range out.Profiles {
			mark := " "
			if p.Default {
				mark = "*"
			}
			fmt.Printf("%s %-16s account %-16s %v\n", mark, p.Name, p.Account, p.Inputs)
		}
		return nil
	case "set", "add":
		if len(args) < 3 {
			return fmt.Errorf("%s", use)
		}
		in, err := inputsOf(args)
		if err != nil {
			return err
		}
		def := false
		for _, a := range args {
			if a == "--default" {
				def = true
			}
		}
		var got struct {
			Default bool `json:"default"`
		}
		body := map[string]any{"account": flagOf(args, "--account", ""), "inputs": in, "default": def}
		if err := call("PUT", "/api/modules/"+url.PathEscape(args[1])+"/profiles/"+url.PathEscape(args[2]), body, &got); err != nil {
			return err
		}
		extra := ""
		if got.Default {
			extra = " (default)"
		}
		fmt.Printf("profile %s/%s set%s\n", args[1], args[2], extra)
		return nil
	}
	return fmt.Errorf("%s", use)
}
