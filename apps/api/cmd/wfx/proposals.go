package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
)

// `wfx workflow …` — git-native workflow proposals. A save or delete on a
// git-backed project opens a branch (and a PR when there is a remote); these
// verbs list them and decide them.
func workflowCmd(args []string) error {
	switch first(args) {
	case "proposals":
		return listProposals(args[1:])
	case "approve":
		return decideProposal(args[1:], "approve", nil)
	case "reject":
		return decideProposal(args[1:], "reject", map[string]any{"reason": flagOf(args, "--reason", flagOf(args, "-m", ""))})
	default:
		return fmt.Errorf("usage: wfx workflow proposals [--project p] [--workflow w] [--status s] | approve <id> | reject <id> --reason \"why\"")
	}
}

type proposalRow struct {
	ID            string   `json:"id"`
	Project       string   `json:"project"`
	Workflow      string   `json:"workflow"`
	Kind          string   `json:"kind"`
	Branch        string   `json:"branch"`
	PRURL         string   `json:"prUrl"`
	Status        string   `json:"status"`
	ReviewVerdict string   `json:"reviewVerdict"`
	ReviewScore   *float64 `json:"reviewScore"`
	CreatedBy     string   `json:"createdBy"`
}

func listProposals(args []string) error {
	q := url.Values{}
	for _, f := range []string{"project", "workflow", "status"} {
		if v := flagOf(args, "--"+f, ""); v != "" {
			q.Set(f, v)
		}
	}
	path := "/api/proposals"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rows []proposalRow
	if err := call("GET", path, nil, &rows); err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Println("no proposals")
		return nil
	}
	fmt.Printf("%-36s %-12s %-16s %-7s %-9s %-16s %s\n", "PROPOSAL", "PROJECT", "WORKFLOW", "KIND", "STATUS", "REVIEW", "PR/BRANCH")
	for _, p := range rows {
		review := p.ReviewVerdict
		if p.ReviewScore != nil {
			review = fmt.Sprintf("%s %.2f", review, *p.ReviewScore)
		}
		where := p.PRURL
		if where == "" {
			where = p.Branch
		}
		fmt.Printf("%-36s %-12s %-16s %-7s %-9s %-16s %s\n", p.ID, p.Project, p.Workflow, p.Kind, p.Status, review, where)
	}
	return nil
}

func decideProposal(args []string, verb string, extra map[string]any) error {
	id := first(args)
	if id == "" || id[0] == '-' {
		return fmt.Errorf("usage: wfx workflow %s <proposal-id>", verb)
	}
	if verb == "reject" && extra["reason"] == "" {
		return fmt.Errorf("usage: wfx workflow reject <proposal-id> --reason \"why\"")
	}
	var out struct {
		Proposal proposalRow `json:"proposal"`
	}
	if err := call("POST", "/api/proposals/"+id+"/"+verb, resolveBody(args, extra), &out); err != nil {
		return err
	}
	fmt.Printf("%s ok — %s is %s\n", verb, out.Proposal.Workflow, out.Proposal.Status)
	return nil
}
