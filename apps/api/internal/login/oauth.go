package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// OAuth is the authorization-code flow for oidc, google and github.
//
// The identity is read from the provider's userinfo endpoint with the access
// token the code was exchanged for — over TLS, by a confidential client, with
// a state the browser had to carry back. That avoids verifying id_token
// signatures (JWKS) entirely, which is the part that usually goes wrong.
type OAuth struct {
	cfg  config.AuthProvider
	http *http.Client

	once                     sync.Once
	discErr                  error
	authURL, tokenURL, uiURL string
}

// NewOAuth applies the google/github presets on top of the config.
func NewOAuth(c config.AuthProvider) (*OAuth, error) {
	switch strings.ToLower(c.Type) {
	case "google":
		c.Issuer = or(c.Issuer, "https://accounts.google.com")
		c.UsernameClaim = or(c.UsernameClaim, "email")
		if len(c.Scopes) == 0 {
			c.Scopes = []string{"openid", "email", "profile"}
		}
	case "github":
		// GitHub is OAuth 2.0, not OIDC: fixed endpoints, no discovery.
		c.AuthURL = or(c.AuthURL, "https://github.com/login/oauth/authorize")
		c.TokenURL = or(c.TokenURL, "https://github.com/login/oauth/access_token")
		c.UserInfoURL = or(c.UserInfoURL, "https://api.github.com/user")
		c.UsernameClaim = or(c.UsernameClaim, "login")
		if len(c.Scopes) == 0 {
			c.Scopes = []string{"read:user"}
		}
	default:
		if len(c.Scopes) == 0 {
			c.Scopes = []string{"openid", "profile", "email"}
		}
	}
	if c.ClientID == "" {
		return nil, errors.New("clientId is required")
	}
	if c.Issuer == "" && (c.AuthURL == "" || c.TokenURL == "" || c.UserInfoURL == "") {
		return nil, errors.New("set issuer (discovery) or authUrl, tokenUrl and userInfoUrl")
	}
	return &OAuth{cfg: c, http: &http.Client{Timeout: 15 * time.Second},
		authURL: c.AuthURL, tokenURL: c.TokenURL, uiURL: c.UserInfoURL}, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// discover fills the endpoints from {issuer}/.well-known/openid-configuration
// once, for whatever the config did not state.
func (o *OAuth) discover(ctx context.Context) error {
	o.once.Do(func() {
		if o.authURL != "" && o.tokenURL != "" && o.uiURL != "" {
			return
		}
		var d struct {
			Auth     string `json:"authorization_endpoint"`
			Token    string `json:"token_endpoint"`
			UserInfo string `json:"userinfo_endpoint"`
		}
		o.discErr = o.getJSON(ctx, strings.TrimRight(o.cfg.Issuer, "/")+"/.well-known/openid-configuration", "", &d)
		o.authURL, o.tokenURL, o.uiURL = or(o.authURL, d.Auth), or(o.tokenURL, d.Token), or(o.uiURL, d.UserInfo)
		if o.discErr == nil && (o.authURL == "" || o.tokenURL == "" || o.uiURL == "") {
			o.discErr = errors.New("discovery document is missing an endpoint")
		}
	})
	return o.discErr
}

func (o *OAuth) AuthCodeURL(ctx context.Context, state, redirectURI string) (string, error) {
	if err := o.discover(ctx); err != nil {
		return "", err
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {o.cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(o.cfg.Scopes, " ")},
		"state":         {state},
	}
	sep := "?"
	if strings.Contains(o.authURL, "?") {
		sep = "&"
	}
	return o.authURL + sep + q.Encode(), nil
}

func (o *OAuth) Exchange(ctx context.Context, code, redirectURI string) (*Identity, error) {
	if err := o.discover(ctx); err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {o.cfg.ClientID},
		"client_secret": {secretFrom(o.cfg.ClientSecretEnv)},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 || json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange refused: %s %s", res.Status, tok.Error)
	}
	var claims map[string]any
	if err := o.getJSON(ctx, o.uiURL, tok.AccessToken, &claims); err != nil {
		return nil, err
	}
	name := ""
	for _, k := range []string{o.cfg.UsernameClaim, "preferred_username", "email", "login", "sub"} {
		if v, ok := claims[k]; ok && k != "" {
			if name = fmt.Sprint(v); name != "" {
				break
			}
		}
	}
	if name == "" {
		return nil, errors.New("the provider named no user")
	}
	var groups []string
	if gs, ok := claims[or(o.cfg.GroupsClaim, "groups")].([]any); ok {
		for _, g := range gs {
			groups = append(groups, fmt.Sprint(g))
		}
	}
	return &Identity{Provider: o.cfg.Name, Username: name, Groups: groups}, nil
}

func (o *OAuth) getJSON(ctx context.Context, u, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s answered %s", u, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
