package login

import (
	"context"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

func TestPresetsAndBuildRules(t *testing.T) {
	ps, errs := Build([]config.AuthProvider{
		{Name: "gh", Type: "github", ClientID: "id"},
		{Name: "g", Type: "google", ClientID: "id"},
		{Name: "dir", Type: "ldap", URL: "ldap://127.0.0.1:1", BaseDN: "dc=x"},
		{Name: "broken", Type: "oidc"}, // no clientId
		{Name: "mock", Type: "mock-ldap", File: "/nonexistent"},
		{Name: "what", Type: "firebase"},
	}, false)
	if len(errs) != 3 || ps["broken"] != nil || ps["mock"] != nil || ps["what"] != nil {
		t.Fatalf("build: %v %v", ps, errs)
	}
	if !strings.Contains(errs[1].Error(), "off loopback") {
		t.Fatalf("mock-ldap off loopback: %v", errs[1])
	}
	u, err := ps["gh"].Redirect.AuthCodeURL(context.Background(), "st", "https://w/cb")
	if err != nil || !strings.HasPrefix(u, "https://github.com/login/oauth/authorize?") || !strings.Contains(u, "state=st") {
		t.Fatalf("github preset: %s %v", u, err)
	}
	if o := ps["g"].Redirect.(*OAuth); o.cfg.Issuer != "https://accounts.google.com" || o.cfg.UsernameClaim != "email" {
		t.Fatalf("google preset: %+v", o.cfg)
	}
	if ps["dir"].Kind() != "password" || ps["gh"].Kind() != "redirect" {
		t.Fatal("kinds")
	}
	// An unreachable directory is a refusal, never a sign-in.
	if _, err := ps["dir"].Password.Authenticate(context.Background(), "ana", "pw"); err == nil {
		t.Fatal("an unreachable LDAP signed someone in")
	}
}
