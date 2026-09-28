package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// `wfx ui` — open the web UI already signed in as the logged-in CLI user
// (ADR 0022).
//
// The server hands back a ONE-TIME link: a code that is already approved by
// this CLI's user, works once, and dies after 60 seconds. Opening it sets an
// HttpOnly session cookie and redirects to /. No token value is ever in the
// URL, on stdout, or passed to the browser process — only that short-lived
// code is.
func uiCmd(args []string) error {
	target, err := resolveContext(flagOf(args, "--url", ""))
	if err != nil {
		return err
	}
	var link struct {
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := callWith(target, "POST", "/api/auth/link", map[string]string{}, &link); err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "unauthenticated") {
			return fmt.Errorf("not signed in to %s — run `wfx login --url %s` first", target.name, target.url)
		}
		return err
	}
	if link.URL == "" {
		return fmt.Errorf("%s did not answer with a sign-in link", target.name)
	}
	if link.ExpiresIn > 0 {
		fmt.Printf("Open the wfnexus UI signed in (one-time link, expires in %ds):\n\n    %s\n\n", link.ExpiresIn, link.URL)
	} else {
		fmt.Printf("Open the wfnexus UI:\n\n    %s\n\n", link.URL)
	}
	if hasFlag(args, "--print") {
		return nil
	}
	if err := openBrowser(link.URL); err != nil {
		fmt.Println("(could not open a browser here — open the link above yourself)")
	}
	return nil
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// approveCode is `wfx login --approve <code>`: this terminal, already signed
// in, approves a device code shown somewhere else — typically the web UI's
// sign-in card. It is the same POST the /device page makes, with this CLI's
// bearer as the approving identity.
func approveCode(args []string, code string) error {
	target, err := resolveContext(flagOf(args, "--url", ""))
	if err != nil {
		return err
	}
	var out struct {
		Status string `json:"status"`
		User   string `json:"user"`
	}
	if err := callWith(target, "POST", "/api/device/verify", map[string]string{
		"user_code": code, "action": "approve",
	}, &out); err != nil {
		return err
	}
	fmt.Printf("Approved %s as %s on %s.\n", strings.ToUpper(code), or(out.User, "?"), target.name)
	return nil
}
