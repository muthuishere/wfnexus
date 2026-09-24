package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// `wfx login` — the OAuth 2.0 Device Authorization Grant, RFC 8628.
//
// It binds NO port and launches NO browser. That is not a simplification, it is
// the requirement: authoring happens inside the author's own Claude Code or
// Codex session, which is an SSH session, a container, or somebody else's
// machine. A redirect flow's callback has nowhere to land there (RFC 8628 §1).
// So the client prints a code and polls, and the human approves from wherever
// they do have a browser.

type deviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Complete        string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceAnswer struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// postDevice talks to the grant endpoints directly rather than through call():
// these are the two routes that exist precisely because there is no credential
// yet, and §3.5 answers a pending authorization with a 400 that is not an error
// to report but a state to keep polling.
func postDevice(host, path string, body map[string]string, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", host+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s — is %s reachable?", err, host)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(res.Body)
	if len(answer) == 0 {
		return fmt.Errorf("%s answered %s with nothing", host, res.Status)
	}
	return json.Unmarshal(answer, out)
}

func login(args []string) error {
	host := normalizeURL(flagOf(args, "--url", ""))
	if host == "" {
		// Without --url there is nothing to log in TO; the default loopback
		// server has no authentication to log in to either.
		return fmt.Errorf("usage: wfx login --url https://wfx.example.com")
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}

	var start deviceStart
	if err := postDevice(host, "/api/device/code", map[string]string{
		"client_id": "wfx-cli",
		"hostname":  hostName(),
	}, &start); err != nil {
		return err
	}
	if start.UserCode == "" {
		return fmt.Errorf("%s did not answer with a device code — is it a wfx server?", host)
	}

	uri := start.VerificationURI
	if start.Complete != "" {
		uri = start.Complete
	}
	// Printed, not opened. Everything a human needs is on stdout, so this works
	// identically when stdout is a pipe in somebody else's agent session.
	fmt.Printf("\nOpen this in a browser (on any machine):\n\n    %s\n\nand enter the code:\n\n    %s\n\n", uri, start.UserCode)
	fmt.Printf("Waiting for approval (expires in %d minutes)...\n", max(start.ExpiresIn/60, 1))

	interval := time.Duration(max(start.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(start.ExpiresIn, 60)) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		var answer deviceAnswer
		if err := postDevice(host, "/api/device/token", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": start.DeviceCode,
		}, &answer); err != nil {
			return err
		}
		switch {
		case answer.AccessToken != "":
			user := whoAmI(host, answer.AccessToken)
			// The value goes straight into the 0600 contexts file. It is not
			// printed, not logged, and not passed to any process.
			if err := putContext(host, answer.AccessToken, user); err != nil {
				return err
			}
			fmt.Printf("Logged in to %s as %s.\n", hostKey(host), or(user, "?"))
			return nil
		case answer.Error == "authorization_pending":
			// §3.5: keep waiting, at the interval the server set.
		case answer.Error == "slow_down":
			// §3.5: add five seconds and never poll faster again.
			interval += 5 * time.Second
		case answer.Error == "access_denied":
			return fmt.Errorf("the sign-in was denied")
		case answer.Error == "expired_token":
			return fmt.Errorf("the code expired before it was approved — run `wfx login --url %s` again", host)
		default:
			return fmt.Errorf("%s: %s", or(answer.Error, "login failed"), answer.Description)
		}
	}
	// Nothing is written on expiry: an unfinished login leaves no credential.
	return fmt.Errorf("the code expired before it was approved — run `wfx login --url %s` again", host)
}

// whoAmI names the user the new credential resolves to, for the context entry.
// A server that does not answer is not a failure: the token works regardless.
func whoAmI(host, token string) string {
	req, err := http.NewRequest("GET", host+"/api/whoami", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var who struct {
		Name string `json:"name"`
	}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &who)
	return who.Name
}

// logout revokes at the host and then forgets locally.
//
// The order matters: removing the local entry without revoking leaves a LIVE
// credential on a server nobody is now tracking. But an unreachable host must
// not trap the credential on this machine either, so the local entry goes
// either way and the failure is reported.
func logout(args []string) error {
	target, err := resolveContext(flagOf(args, "--url", ""))
	if err != nil {
		return err
	}
	if target.token == "" {
		return fmt.Errorf("not logged in to %s", target.name)
	}
	revokeErr := callWith(target, "DELETE", "/api/tokens/self", nil, nil)
	if err := removeContext(target.name); err != nil {
		return err
	}
	if revokeErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %s could not be reached to revoke the token (%v);\n"+
			"the local credential is gone, but revoke it there when you can.\n", target.name, revokeErr)
	}
	fmt.Printf("Logged out of %s.\n", target.name)
	return nil
}
