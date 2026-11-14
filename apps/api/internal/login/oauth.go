package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// OAuth is the authorization-code flow for oidc, google and github.
//
// Every provider uses PKCE (S256): the browser round trip carries only the
// challenge, and the code is worth nothing without the verifier kept between
// start and callback.
//
// For OpenID Connect providers (oidc, google) the token response must carry an
// id_token, which is verified before anything else is trusted: its signature
// (RS256 or ES256) against the issuer's published keys (jwks_uri), its issuer,
// audience, expiry, issue time and the nonce sent at start. The identity is
// then read from userinfo with the access token, and userinfo must name the
// same subject as the id_token. GitHub is OAuth 2.0 only and issues no
// id_token; it gets PKCE and the userinfo read.
type OAuth struct {
	cfg  config.AuthProvider
	http *http.Client

	once                     sync.Once
	discErr                  error
	authURL, tokenURL, uiURL string
	issuer                   string
	keys                     *keySet
}

// AuthRequest is one sign-in's single-use values, made at start and kept by
// the caller (in cookies) until the callback.
type AuthRequest struct {
	State    string
	Verifier string // PKCE code_verifier, 43 characters
	Nonce    string // sent to OIDC providers, echoed in the id_token
}

// NewAuthRequest makes fresh random values for one sign-in.
func NewAuthRequest() AuthRequest {
	return AuthRequest{State: randomHex(24), Verifier: randomB64(32), Nonce: randomB64(24)}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge is the S256 code_challenge for a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
	o := &OAuth{cfg: c, http: &http.Client{Timeout: 15 * time.Second},
		authURL: c.AuthURL, tokenURL: c.TokenURL, uiURL: c.UserInfoURL}
	if o.oidc() {
		// The id_token is checked against the issuer and its published keys,
		// so an OpenID provider needs one; endpoints in the config still win
		// over discovered ones.
		if c.Issuer == "" {
			return nil, errors.New("issuer is required for an OpenID Connect provider (its id_token is verified against it)")
		}
		if !slices.Contains(o.cfg.Scopes, "openid") {
			o.cfg.Scopes = append([]string{"openid"}, o.cfg.Scopes...)
		}
	} else if c.Issuer == "" && (c.AuthURL == "" || c.TokenURL == "" || c.UserInfoURL == "") {
		return nil, errors.New("set issuer (discovery) or authUrl, tokenUrl and userInfoUrl")
	}
	return o, nil
}

// oidc says whether this provider issues an id_token to verify.
func (o *OAuth) oidc() bool { return strings.ToLower(o.cfg.Type) != "github" }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// discover reads {issuer}/.well-known/openid-configuration once: the
// endpoints the config did not state, and for an OpenID provider its issuer
// (which must be the configured one) and jwks_uri.
func (o *OAuth) discover(ctx context.Context) error {
	o.once.Do(func() {
		if !o.oidc() && o.authURL != "" && o.tokenURL != "" && o.uiURL != "" {
			return
		}
		var d struct {
			Issuer   string `json:"issuer"`
			Auth     string `json:"authorization_endpoint"`
			Token    string `json:"token_endpoint"`
			UserInfo string `json:"userinfo_endpoint"`
			JWKS     string `json:"jwks_uri"`
		}
		o.discErr = o.getJSON(ctx, strings.TrimRight(o.cfg.Issuer, "/")+"/.well-known/openid-configuration", "", &d)
		if o.discErr != nil {
			return
		}
		o.authURL, o.tokenURL, o.uiURL = or(o.authURL, d.Auth), or(o.tokenURL, d.Token), or(o.uiURL, d.UserInfo)
		if o.authURL == "" || o.tokenURL == "" || o.uiURL == "" {
			o.discErr = errors.New("discovery document is missing an endpoint")
			return
		}
		if o.oidc() {
			if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(o.cfg.Issuer, "/") {
				o.discErr = fmt.Errorf("discovery names issuer %q, configured %q", d.Issuer, o.cfg.Issuer)
				return
			}
			if d.JWKS == "" {
				o.discErr = errors.New("discovery document has no jwks_uri")
				return
			}
			o.issuer = d.Issuer
			o.keys = &keySet{url: d.JWKS, client: o.http}
		}
	})
	return o.discErr
}

func (o *OAuth) AuthCodeURL(ctx context.Context, a AuthRequest, redirectURI string) (string, error) {
	if err := o.discover(ctx); err != nil {
		return "", err
	}
	if a.State == "" || len(a.Verifier) < 43 || len(a.Verifier) > 128 {
		return "", errors.New("a sign-in needs a state and a 43-128 character PKCE verifier")
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(o.cfg.Scopes, " ")},
		"state":                 {a.State},
		"code_challenge":        {pkceChallenge(a.Verifier)},
		"code_challenge_method": {"S256"},
	}
	if o.oidc() {
		if a.Nonce == "" {
			return "", errors.New("an OpenID sign-in needs a nonce")
		}
		q.Set("nonce", a.Nonce)
	}
	sep := "?"
	if strings.Contains(o.authURL, "?") {
		sep = "&"
	}
	return o.authURL + sep + q.Encode(), nil
}

func (o *OAuth) Exchange(ctx context.Context, code, redirectURI string, a AuthRequest) (*Identity, error) {
	if err := o.discover(ctx); err != nil {
		return nil, err
	}
	if a.Verifier == "" || (o.oidc() && a.Nonce == "") {
		return nil, errors.New("sign-in values are missing; start again")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {o.cfg.ClientID},
		"client_secret": {secretFrom(o.cfg.ClientSecretEnv)},
		"code_verifier": {a.Verifier},
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
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 || json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange refused: %s %s", res.Status, tok.Error)
	}
	var subject string
	if o.oidc() {
		if tok.IDToken == "" {
			return nil, errors.New("the provider returned no id_token")
		}
		idc, err := verifyIDToken(ctx, tok.IDToken, o.keys, o.issuer, o.cfg.ClientID, a.Nonce, time.Now())
		if err != nil {
			return nil, fmt.Errorf("id_token refused: %w", err)
		}
		subject = idc.Subject
	}
	var claims map[string]any
	if err := o.getJSON(ctx, o.uiURL, tok.AccessToken, &claims); err != nil {
		return nil, err
	}
	if o.oidc() {
		if sub, _ := claims["sub"].(string); sub == "" || subtle.ConstantTimeCompare([]byte(sub), []byte(subject)) != 1 {
			return nil, errors.New("userinfo names a different subject than the id_token")
		}
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
