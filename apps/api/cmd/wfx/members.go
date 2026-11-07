package main

import (
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"
)

// `wfx member` (ADR 0028): grant the scoped roles. A project admin manages
// its project's members, a workflow admin its workflow's; superadmin is set
// with `wfx user role NAME superadmin`.
const memberUsage = `usage:
  wfx member ls  --project P | --workflow W
  wfx member add USER --project P  [--role project_admin]
  wfx member add USER --workflow W [--role workflow_admin|workflow_runner]   (default workflow_runner)
  wfx member rm  USER --project P | --workflow W`

type apiMember struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	User  string `json:"user"`
	Role  string `json:"role"`
}

func memberPath(args []string) (string, error) {
	p, w := flagOf(args, "--project", ""), flagOf(args, "--workflow", "")
	switch {
	case p != "" && w == "":
		return "/api/projects/" + url.PathEscape(p) + "/members", nil
	case w != "" && p == "":
		return "/api/workflows/" + url.PathEscape(w) + "/members", nil
	}
	return "", fmt.Errorf("give exactly one of --project or --workflow\n%s", memberUsage)
}

func memberCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", memberUsage)
	}
	path, err := memberPath(args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "ls", "list":
		var ms []apiMember
		if err := call("GET", path, nil, &ms); err != nil {
			return err
		}
		if hasFlag(args, "--json") {
			return printJSON(ms)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "USER\tROLE")
		for _, m := range ms {
			fmt.Fprintf(tw, "%s\t%s\n", m.User, m.Role)
		}
		return tw.Flush()
	case "add", "rm", "remove":
		if len(args) < 2 || args[1] == "" || args[1][0] == '-' {
			return fmt.Errorf("%s", memberUsage)
		}
		target := path + "/" + url.PathEscape(args[1])
		if args[0] == "add" {
			var out apiMember
			if err := call("PUT", target, map[string]string{"role": flagOf(args, "--role", "")}, &out); err != nil {
				return err
			}
			fmt.Printf("%s is %s on %s %s\n", out.User, out.Role, out.Scope, out.Name)
			return nil
		}
		if err := call("DELETE", target, nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[1])
		return nil
	}
	return fmt.Errorf("%s", memberUsage)
}
