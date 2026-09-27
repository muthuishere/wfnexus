package main

import (
	"fmt"
	"net/url"
	"strings"
)

// adapters prints every ACP adapter and whether this machine can run it.
func adapters() error {
	var as []struct {
		Name, Bin, ModelFlag, Mode, Install, Path string
		Argv                                      []string
		Installed                                 bool
		Providers                                 []string
	}
	if err := call("GET", "/api/adapters", nil, &as); err != nil {
		return err
	}
	for _, a := range as {
		how := "model over the protocol"
		if a.ModelFlag != "" {
			how = "model via " + a.ModelFlag
		}
		fmt.Printf("%s %-9s %s %s  (%s, mode %s)\n", tick(a.Installed, "✓", "✗"), a.Name, a.Bin, strings.Join(a.Argv, " "), how, a.Mode)
		if len(a.Providers) > 0 {
			fmt.Printf("            providers: %s\n", strings.Join(a.Providers, ", "))
		}
		if !a.Installed {
			fmt.Printf("            install:   %s\n", a.Install)
		}
	}
	return nil
}

// providerModels asks an acp provider's agent which models it offers.
func providerModels(args []string) error {
	name := first(args)
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("usage: wfx models <provider> [--free]   (an acp provider; `wfx adapters` lists them)")
	}
	free := hasFlag(args, "--free")
	var m struct {
		Provider, Current, Configured string
		Offered                       bool
		Models                        []struct {
			ID, Name string
			Free     bool
		}
	}
	if err := call("GET", "/api/providers/"+url.PathEscape(name)+"/models", nil, &m); err != nil {
		return err
	}
	n := 0
	for _, x := range m.Models {
		if free && !x.Free {
			continue
		}
		mark := " "
		if x.ID == m.Current {
			mark = "*"
		}
		tag := ""
		if x.Free {
			tag = "  free"
		}
		fmt.Printf("%s %s%s\n", mark, x.ID, tag)
		n++
	}
	fmt.Printf("\n%d model(s)%s; * = the agent's default\n", n, map[bool]string{true: " (free only)"}[free])
	if m.Configured != "" {
		fmt.Printf("%s is configured for %s — %s\n", m.Provider, m.Configured, tick(m.Offered, "offered ✓", "NOT offered by this agent: a step on it will refuse to start"))
	}
	return nil
}
