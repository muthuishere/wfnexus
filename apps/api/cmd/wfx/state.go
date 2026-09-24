package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// `wfx state` — what a workflow REMEMBERS between runs.
//
//	wfx state set --workflow last_id 4121
//	wfx state get --step  cursor
//	wfx state list --global
//
// ONE VERB, FOUR SCOPE FLAGS, not four verbs. The operation is the same
// operation — get, set, list, rm — and the scope is an adjective on it, so
// `wfx state set --global` and `wfx state set --workflow` stay visibly the same
// thing done in a different place. Four verbs (`wfx global set`, `wfx
// project-state set`) would have been four commands to document, and the odd
// one out — the project scope — would have had to be spelled differently
// anyway.
//
// INSIDE A RUN the scope's owner is never passed. A step exports WFX_RUN_ID and
// WFX_STEP_ID; the CLI sends those, and the SERVER reads the workflow name and
// the project off the run row. That is what stops one workflow writing another
// repository's state — not a convention, an absent parameter.
//
// OUTSIDE A RUN — a person at a terminal — there is no run to read, so the
// owner is given explicitly with `--name`.
//
// NOT A SECRET STORE. Values are stored in plaintext and are echoed back by
// every one of these commands. A token goes in `wfx env`, which is sealed.

type stateVar struct {
	Scope     string    `json:"scope"`
	ScopeName string    `json:"scopeName"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type stateList struct {
	Scope     string     `json:"scope"`
	ScopeName string     `json:"scopeName"`
	Vars      []stateVar `json:"vars"`
}

const stateUsage = `usage: wfx state <get|set|list|rm> --step|--workflow|--project|--global [--name OWNER] [KEY [VALUE]]`

func stateCmd(args []string) error {
	scope, err := stateScopeOf(args)
	if err != nil {
		return err
	}
	name := flagOf(args, "--name", "")
	rest := withoutFlag(dropFlags(args, "--step", "--workflow", "--project", "--global"), "--name")

	verb := "list"
	if len(rest) > 0 {
		verb = rest[0]
		rest = rest[1:]
	}
	switch verb {
	case "list":
		return stateShow(scope, name)
	case "get":
		if len(rest) < 1 {
			return fmt.Errorf("usage: wfx state get --%s KEY", scope)
		}
		return stateGet(scope, name, rest[0])
	case "set":
		if len(rest) < 2 {
			return fmt.Errorf("usage: wfx state set --%s KEY VALUE", scope)
		}
		return stateSet(scope, name, rest[0], strings.Join(rest[1:], " "))
	case "rm", "unset", "delete":
		if len(rest) < 1 {
			return fmt.Errorf("usage: wfx state rm --%s KEY", scope)
		}
		return stateRemove(scope, name, rest[0])
	}
	return fmt.Errorf("%s", stateUsage)
}

// stateScopeOf insists on exactly one scope. Defaulting to one of them would
// mean a mistyped flag wrote somewhere the author did not mean, and these
// namespaces do not fall back to one another precisely so that a wrong place is
// visible rather than plausible.
func stateScopeOf(args []string) (string, error) {
	var found []string
	for _, s := range []string{"step", "workflow", "project", "global"} {
		if hasFlag(args, "--"+s) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("which scope? --step, --workflow, --project or --global\n%s", stateUsage)
	}
	return "", fmt.Errorf("one scope at a time — you gave %s", strings.Join(found, " and "))
}

// stateBase is the URL to talk to, and it is the whole security story: inside a
// run it is the run's own door, which takes a scope and no owner.
func stateBase(scope, name string) (string, string, error) {
	runID, stepID := os.Getenv("WFX_RUN_ID"), os.Getenv("WFX_STEP_ID")
	if runID != "" && name == "" {
		q := "?scope=" + url.QueryEscape(scope)
		if scope == "step" {
			q += "&stepId=" + url.QueryEscape(stepID)
		}
		return "/api/runs/" + url.PathEscape(runID) + "/state", q, nil
	}
	if scope != "global" && name == "" {
		return "", "", fmt.Errorf("outside a run, %s state needs --name OWNER", scope)
	}
	return "/api/state", "?scope=" + url.QueryEscape(scope) + "&name=" + url.QueryEscape(name), nil
}

func stateShow(scope, name string) error {
	path, q, err := stateBase(scope, name)
	if err != nil {
		return err
	}
	var out stateList
	if err := call("GET", path+q, nil, &out); err != nil {
		return err
	}
	if len(out.Vars) == 0 {
		fmt.Printf("nothing in %s state (%s)\n", scope, or(out.ScopeName, "—"))
		return nil
	}
	fmt.Printf("%-28s %s\n", "KEY", "VALUE")
	for _, v := range out.Vars {
		fmt.Printf("%-28s %s\n", v.Key, v.Value)
	}
	return nil
}

func stateGet(scope, name, key string) error {
	path, q, err := stateBase(scope, name)
	if err != nil {
		return err
	}
	var out stateList
	if err := call("GET", path+q, nil, &out); err != nil {
		return err
	}
	for _, v := range out.Vars {
		if v.Key == key {
			fmt.Println(v.Value)
			return nil
		}
	}
	// Nothing, and that is not an error: "I have never run before" is the
	// normal first answer for the case this store exists to serve. It prints
	// nothing so `X=$(wfx state get --workflow last_id)` gives an empty string.
	return nil
}

func stateSet(scope, name, key, value string) error {
	path, _, err := stateBase(scope, name)
	if err != nil {
		return err
	}
	body := map[string]any{"scope": scope, "key": key, "value": value}
	method := "PUT"
	if strings.Contains(path, "/runs/") {
		method = "POST"
		body["stepId"] = os.Getenv("WFX_STEP_ID")
	} else {
		body["name"] = name
	}
	if err := call(method, path, body, nil); err != nil {
		return err
	}
	fmt.Printf("%s state: %s = %s\n", scope, key, value)
	return nil
}

func stateRemove(scope, name, key string) error {
	path, q, err := stateBase(scope, name)
	if err != nil {
		return err
	}
	if err := call("DELETE", path+"/"+url.PathEscape(key)+q, nil, nil); err != nil {
		return err
	}
	fmt.Printf("removed %s from %s state\n", key, scope)
	return nil
}

// dropFlags removes bare flags (no value follows them).
func dropFlags(args []string, names ...string) []string {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	var out []string
	for _, a := range args {
		if !drop[a] {
			out = append(out, a)
		}
	}
	return out
}

