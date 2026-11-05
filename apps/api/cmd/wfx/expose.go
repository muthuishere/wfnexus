package main

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// `wfx expose` — let something OUTSIDE start a workflow (ADR 0027).
//
//	wfx expose hook <wf>              issue the hook token (shown once) and print the URL
//	wfx expose mcp <wf>               issue the MCP token and print the client config snippet
//	wfx expose hook|mcp <wf> --rotate new token, the old one stops working
//	wfx expose hook|mcp <wf> --revoke switch it off
//	wfx expose mcp --project P        one project token for every opted-in workflow
//	wfx expose status <wf>            what is exposed (never a token)

const exposeUsage = `usage: wfx expose hook|mcp <workflow> [--rotate|--revoke] | wfx expose mcp --project P [--rotate|--revoke] | wfx expose status <workflow>`

func exposeCmd(args []string) error {
	project := flagOf(args, "--project", "")
	rest := withoutFlag(args, "--project")
	rotate, revoke := hasFlag(rest, "--rotate"), hasFlag(rest, "--revoke")
	var pos []string
	for _, a := range rest {
		if a != "--rotate" && a != "--revoke" {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return fmt.Errorf("%s", exposeUsage)
	}
	kind := pos[0]
	wf := ""
	if len(pos) > 1 {
		wf = pos[1]
	}
	if kind == "status" {
		if wf == "" {
			return fmt.Errorf("which workflow?\n%s", exposeUsage)
		}
		var out map[string]any
		if err := call("GET", "/api/workflows/"+url.PathEscape(wf)+"/expose", nil, &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	if kind != "hook" && kind != "mcp" {
		return fmt.Errorf("%s", exposeUsage)
	}
	var base string
	switch {
	case wf != "":
		base = "/api/workflows/" + url.PathEscape(wf) + "/expose/" + kind
	case project != "" && kind == "mcp":
		base = "/api/projects/" + url.PathEscape(project) + "/expose/mcp"
	default:
		return fmt.Errorf("which workflow? (a project token is mcp only: wfx expose mcp --project P)\n%s", exposeUsage)
	}
	if revoke {
		var out struct {
			Revoked int `json:"revoked"`
		}
		if err := call("DELETE", base, nil, &out); err != nil {
			return err
		}
		fmt.Printf("revoked %d %s token(s); %s is no longer exposed that way\n", out.Revoked, kind, firstNonEmpty(wf, project))
		return nil
	}
	var out map[string]any
	if err := call("POST", base, map[string]bool{"rotate": rotate}, &out); err != nil {
		return err
	}
	url := ""
	if v, ok := out[kind].(map[string]any); ok {
		url, _ = v["url"].(string)
	}
	if u, ok := out["url"].(string); ok && url == "" {
		url = u
	}
	fmt.Printf("%s exposed: %s\n", kind, url)
	fmt.Printf("token (shown ONCE — store it now): %v\n", out["token"])
	if sig, ok := out["signingSecret"].(string); ok && sig != "" {
		fmt.Printf("signing secret for X-WFX-Signature HMAC (shown ONCE): %s\n", sig)
	} else if note, ok := out["signingNote"].(string); ok {
		fmt.Printf("note: %s\n", note)
	}
	fmt.Println()
	switch snip := out["snippet"].(type) {
	case string:
		fmt.Println(snip)
	default:
		b, _ := json.MarshalIndent(snip, "", "  ")
		fmt.Println(string(b))
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
