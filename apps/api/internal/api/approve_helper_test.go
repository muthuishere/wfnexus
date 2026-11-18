package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// approveOnce answers a write command's approval pause as a person would, once,
// then waits for the run to settle and returns its status and the stdout of
// its last step that produced one. A run that is not paused is returned as is.
func approveOnce(t *testing.T, h http.Handler, runID, status, stdout string) (string, string) {
	t.Helper()
	if status != "awaiting_approval" || runID == "" {
		return status, stdout
	}
	if w := do(h, "POST", "/api/runs/"+runID+"/approve", "", map[string]any{"actor": "tester"}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := do(h, "GET", "/api/runs/"+runID, "", nil)
		var res struct {
			Run struct {
				Status string `json:"status"`
			} `json:"run"`
			Steps []struct {
				Output json.RawMessage `json:"output"`
			} `json:"steps"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		switch res.Run.Status {
		case "queued", "running", "":
			time.Sleep(50 * time.Millisecond)
			continue
		}
		out := ""
		for _, s := range res.Steps {
			var o struct {
				Stdout string `json:"stdout"`
			}
			if json.Unmarshal(s.Output, &o) == nil && o.Stdout != "" {
				out = o.Stdout
			}
		}
		return res.Run.Status, out
	}
	t.Fatalf("run %s did not settle after approval", runID)
	return "", ""
}

// callPaused calls a module command and returns its status without answering
// a pause: what a person sees before saying yes.
func callPaused(t *testing.T, h http.Handler, name string, inputs map[string]any) string {
	t.Helper()
	w := do(h, "POST", "/api/modules/"+name+"/call", "", map[string]any{"inputs": inputs, "wait": true})
	if w.Code != 201 {
		t.Fatalf("%s: %d %s", name, w.Code, w.Body)
	}
	var res struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	return res.Run.Status
}

// waitStatus waits until a run reaches a status (or settles elsewhere) and
// returns the status it has.
func waitStatus(t *testing.T, h http.Handler, runID, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := do(h, "GET", "/api/runs/"+runID, "", nil)
		var res struct {
			Run struct {
				Status string `json:"status"`
			} `json:"run"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Run.Status == want || (res.Run.Status != "queued" && res.Run.Status != "running" && res.Run.Status != "") {
			return res.Run.Status
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ""
}
