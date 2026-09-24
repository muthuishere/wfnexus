package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/muthuishere/wfnexus/apps/api/internal/buildinfo"
)

// `wfx version` — which binary this is.
//
// It answers from the binary itself and never calls the server: the question
// "which client am I running" has to be answerable on a machine that is not
// logged in, is offline, or is talking to a host that is down. `--json` is for
// a bug-report template or a CI check that wants to compare rather than read.
func printVersion(args []string) error {
	info := buildinfo.Get()

	if containsStr(args, "--json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	fmt.Println("wfx " + info.String())
	return nil
}
