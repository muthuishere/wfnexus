package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
)

// `wfx prices` — what each model family costs per million tokens (ADR 0020).
// The table is seeded with approximate list prices; set what you actually pay.

type modelPrice struct {
	Model  string  `json:"model"`
	In     float64 `json:"in"`
	Out    float64 `json:"out"`
	Seeded bool    `json:"seeded"`
}

func pricesCmd(args []string) error {
	switch {
	case len(args) == 0 || args[0] == "list":
		return pricesList()
	case args[0] == "set" && len(args) == 4:
		in, err1 := strconv.ParseFloat(args[2], 64)
		out, err2 := strconv.ParseFloat(args[3], 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("IN and OUT are dollars per million tokens, e.g. wfx prices set claude-sonnet 3 15")
		}
		body := map[string]any{"model": args[1], "in": in, "out": out}
		if err := call("PUT", "/api/prices", body, nil); err != nil {
			return err
		}
		fmt.Printf("%s: $%g in / $%g out per 1M tokens\n", args[1], in, out)
		return nil
	case (args[0] == "rm" || args[0] == "reset") && len(args) == 2:
		if err := call("DELETE", "/api/prices?model="+url.QueryEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s: removed (a built-in family is back at its approximate default)\n", args[1])
		return nil
	}
	return fmt.Errorf("usage: wfx prices [list | set MODEL IN OUT | rm MODEL]   (MODEL * is the fallback)")
}

func pricesList() error {
	var out struct {
		Prices []modelPrice `json:"prices"`
	}
	if err := call("GET", "/api/prices", nil, &out); err != nil {
		return err
	}
	sort.Slice(out.Prices, func(i, j int) bool { return out.Prices[i].Model < out.Prices[j].Model })
	fmt.Printf("%-24s %10s %10s  %s\n", "MODEL", "IN $/1M", "OUT $/1M", "SOURCE")
	for _, p := range out.Prices {
		src := "set"
		if p.Seeded {
			src = "approx"
		}
		name := p.Model
		if name == "*" {
			name = "* (fallback)"
		}
		fmt.Printf("%-24s %10g %10g  %s\n", name, p.In, p.Out, src)
	}
	return nil
}
