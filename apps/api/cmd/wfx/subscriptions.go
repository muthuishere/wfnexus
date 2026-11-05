package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// Subscriptions (ADR 0026): `wfx sub add|update|ls|show|pause|resume|rm|validate`.
// Every verb goes through /api/subscriptions, so the CLI and the UI change
// the same registries.json the server hot-reloads.

const subUsage = `usage:
  wfx sub ls [--project p] [--json]
  wfx sub show <name> [--json]
  wfx sub add <name> --channel c --chat <jid|*> --workflow w [options]
  wfx sub update <name> [options]
  wfx sub pause|resume|validate|rm <name>
options:
  --events message,reaction,updated,deleted   (default message)
  --interval 30s|1m|2m                         (default 1m)
  --mode fanout|exclusive|observe              (default exclusive when it replies)
  --activation mention|always                  (groups default mention)
  --allow id,id | '*'                          (empty = nobody)
  --policy draft|approval|auto|none
  --instructions text   --memory m   --skills a,b   --version v   --owner o`

type subRow struct {
	Name           string   `json:"name"`
	Channel        string   `json:"channel"`
	Chat           string   `json:"chat"`
	Events         []string `json:"events,omitempty"`
	Interval       string   `json:"interval,omitempty"`
	Workflow       string   `json:"workflow"`
	Version        string   `json:"version,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	Instructions   string   `json:"instructions,omitempty"`
	ResponsePolicy string   `json:"responsePolicy,omitempty"`
	Memory         string   `json:"memory,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Activation     string   `json:"activation,omitempty"`
	Allow          []string `json:"allow,omitempty"`
	Owner          string   `json:"owner,omitempty"`
	Status         string   `json:"status,omitempty"`
	Error          string   `json:"error,omitempty"`
	StartedAt      string   `json:"startedAt,omitempty"`
	EffectiveMode  string   `json:"effectiveMode,omitempty"`
	Problem        string   `json:"problem,omitempty"`
}

// applySubFlags overlays the flags that were given onto r.
func applySubFlags(r *subRow, args []string) {
	str := map[string]*string{
		"--channel": &r.Channel, "--chat": &r.Chat, "--workflow": &r.Workflow, "--interval": &r.Interval,
		"--version": &r.Version, "--instructions": &r.Instructions, "--policy": &r.ResponsePolicy,
		"--memory": &r.Memory, "--mode": &r.Mode, "--activation": &r.Activation, "--owner": &r.Owner,
	}
	for flag, p := range str {
		if has(args, flag) {
			*p = flagOf(args, flag, "")
		}
	}
	list := map[string]*[]string{"--events": &r.Events, "--skills": &r.Skills, "--allow": &r.Allow}
	for flag, p := range list {
		if has(args, flag) {
			*p = splitList(flagOf(args, flag, ""))
		}
	}
}

func subCmd(args []string) error {
	verb := first(args)
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	name := first(rest)
	needName := func() error {
		if name == "" || strings.HasPrefix(name, "-") {
			return fmt.Errorf("%s", subUsage)
		}
		return nil
	}
	path := func() string { return "/api/subscriptions/" + url.PathEscape(name) }
	switch verb {
	case "", "ls", "list":
		q := ""
		if p := flagOf(args, "--project", ""); p != "" {
			q = "?project=" + url.QueryEscape(p)
		}
		var rows []subRow
		if err := call("GET", "/api/subscriptions"+q, nil, &rows); err != nil {
			return err
		}
		if has(args, "--json") {
			return json.NewEncoder(os.Stdout).Encode(rows)
		}
		if len(rows) == 0 {
			fmt.Println("no subscriptions — add one with `wfx sub add <name> --channel c --chat <jid> --workflow w`")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATUS\tCHANNEL\tCHAT\tWORKFLOW\tMODE\tEVERY\tALLOW\tNOTE")
		for _, r := range rows {
			note := r.Error
			if note == "" {
				note = r.Problem
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.Name, r.Status, r.Channel, r.Chat, r.Workflow,
				r.EffectiveMode, or(r.Interval, "1m"), len(r.Allow), note)
		}
		return tw.Flush()
	case "show", "get":
		if err := needName(); err != nil {
			return err
		}
		var out json.RawMessage
		if err := call("GET", path(), nil, &out); err != nil {
			return err
		}
		var pretty any
		_ = json.Unmarshal(out, &pretty)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	case "add", "update":
		if err := needName(); err != nil {
			return err
		}
		var r subRow
		if verb == "update" {
			var cur struct {
				Subscription subRow `json:"subscription"`
			}
			if err := call("GET", path(), nil, &cur); err != nil {
				return err
			}
			r = cur.Subscription
		}
		applySubFlags(&r, rest)
		var out subRow
		if err := call("PUT", path(), r, &out); err != nil {
			return err
		}
		return printSub(out)
	case "pause", "resume", "validate":
		if err := needName(); err != nil {
			return err
		}
		var out subRow
		if err := call("POST", path()+"/"+verb, nil, &out); err != nil {
			return err
		}
		return printSub(out)
	case "rm", "delete", "remove":
		if err := needName(); err != nil {
			return err
		}
		if err := call("DELETE", path(), nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed subscription %s\n", name)
		return nil
	}
	return fmt.Errorf("%s", subUsage)
}

func printSub(r subRow) error {
	fmt.Printf("%s: %s (%s on %s/%s → %s)\n", r.Name, r.Status, r.EffectiveMode, r.Channel, r.Chat, r.Workflow)
	if r.Error != "" {
		fmt.Printf("  error: %s\n", r.Error)
	} else if r.Problem != "" {
		fmt.Printf("  problem: %s\n", r.Problem)
	}
	if len(r.Allow) == 0 && r.EffectiveMode != "observe" {
		fmt.Println("  note: the allow list is empty, so no sender can start a run (use --allow)")
	}
	return nil
}
