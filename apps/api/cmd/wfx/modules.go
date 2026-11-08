package main

import (
	"fmt"
	"strings"
)

// `wfx module` — modules (ADR 0028): a task + module.yaml, installed from
// the existing registry (a skill source: git import, pin, Sync).
//
//	wfx module ls
//	wfx module install <git-url> [--ref tag|sha|branch] [--name n]   (= wfx skills import --path .)
//	wfx module sync <source>                                          (= wfx skills refresh)
//	wfx module config <module>      prompts once for each missing env var / secret

type moduleRow struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Requires struct {
		Env     []string `json:"env"`
		Secrets []string `json:"secrets"`
	} `json:"requires"`
	Binary *struct {
		Carry string `json:"carry"`
	} `json:"binary"`
	Missing []string `json:"missing"`
	Ready   bool     `json:"ready"`
}

type moduleList struct {
	Modules []moduleRow `json:"modules"`
	Errors  []string    `json:"errors"`
}

func moduleCmd(args []string) error {
	const use = "usage: wfx module ls | install <git-url> [--ref r] [--name n] | sync <source> | config <module>"
	if len(args) == 0 || args[0] == "ls" || args[0] == "list" {
		return moduleLs()
	}
	switch args[0] {
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
		fmt.Printf("%-28s %-10s %-8s %-16s %s\n", "MODULE", "VERSION", "KIND", "SOURCE", "STATUS")
		for _, m := range out.Modules {
			st := "ready"
			if !m.Ready {
				st = "needs " + strings.Join(m.Missing, ", ")
			}
			fmt.Printf("%-28s %-10s %-8s %-16s %s\n", m.Name, m.Version, m.Kind, m.Source, st)
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
