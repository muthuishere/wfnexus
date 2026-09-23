package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// `wfx workers` answers the two questions an operator has about the pool: who
// is out there, and what do I type on the next machine.

type workerRow struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Labels   []string  `json:"labels"`
	OS       string    `json:"os"`
	Arch     string    `json:"arch"`
	Version  string    `json:"version"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

type poolView struct {
	Workers     []workerRow `json:"workers"`
	LocalLabels []string    `json:"localLabels"`
	JoinCommand string      `json:"joinCommand"`
	URL         string      `json:"url"`
}

func workers(args []string) error {
	switch {
	case len(args) >= 2 && (args[0] == "rm" || args[0] == "remove"):
		if err := call("DELETE", "/api/workers/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s — it can join again with the command from `wfx workers`\n", args[1])
		return nil
	case len(args) >= 1 && (args[0] == "rotate" || args[0] == "rotate-token"):
		var out poolView
		if err := call("POST", "/api/workers/token/rotate", nil, &out); err != nil {
			return err
		}
		fmt.Println("the old join command no longer works. Machines already joined are unaffected.")
		fmt.Println("\n  " + out.JoinCommand)
		return nil
	}

	var pool poolView
	if err := call("GET", "/api/workers", nil, &pool); err != nil {
		return err
	}
	if len(pool.Workers) == 0 {
		fmt.Println("No machines have joined.")
	} else {
		fmt.Printf("%-20s %-8s %-14s %-28s %s\n", "WORKER", "STATUS", "PLATFORM", "LABELS", "LAST SEEN")
		for _, w := range pool.Workers {
			fmt.Printf("%-20s %-8s %-14s %-28s %s\n",
				w.Name, w.Status, w.OS+"/"+w.Arch, strings.Join(w.Labels, ","), ago(w.LastSeen))
		}
	}
	// The labels the platform serves itself need no machine at all, and saying
	// so is what stops someone adding a worker to fix a run that was never
	// waiting for one.
	fmt.Printf("\nServed by the platform itself: %s\n", strings.Join(pool.LocalLabels, ", "))
	fmt.Printf("\nTo add a machine, run this on it:\n\n  %s\n", pool.JoinCommand)
	return nil
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < 2*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Format("2006-01-02")
}
