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

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// OAuth is the authorization-code flow for oidc, google and github.
//
// Every provider uses PKCE (S256): the browser round trip carries only the
// challenge, and the code is worth nothing without the verifier kept between
// start and callback.
//
// For OpenID Connect providers (oidc, google) discovery must name exactly the
// configured issuer, and the token response must carry an id_token, verified
// with go-oidc before anything else is trusted: signature (RS256 or ES256)
// against the issuer's published keys, issuer, audience and expiry. On top of
// that: iat present and not in the future, azp equal to the client when present
// (and present when there are several audiences), and the nonce sent at start.
// The identity is then read from userinfo with the access token, and userinfo
// must name the same subject. GitHub is OAuth 2.0 only and issues no id_token;
// it gets PKCE and the userinfo read.
//
// A person is identified by the provider's subject for them — never by a name
// or an email, which can change or be claimed by someone else
// (Identity.Subject).
//
// Errors from the provider are returned to the caller to log; they are not
// meant for the browser.
type OAuth struct {
	cfg  config.AuthProvider
	http *http.Client

	once                     sync.Once
	discErr                  error
	authURL, tokenURL, uiURL string
	verifier                 *oidc.IDTokenVerifier
}

// idTokenSkew is the clock difference allowed on iat.
const idTokenSkew = 60 * time.Second

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

// discover runs once. An OpenID provider is discovered with go-oidc, which
// requires the discovery document's issuer to equal the configured issuer
// exactly; endpoints in the config still win over discovered ones. A provider
// with all three endpoints configured and no id_token (github) needs none.
func (o *OAuth) discover(ctx context.Context) error {
	o.once.Do(func() {
		if !o.oidc() {
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
		} else {
			p, err := oidc.NewProvider(oidc.ClientContext(ctx, o.http), o.cfg.Issuer)
			if err != nil {
				o.discErr = err
				return
			}
			ep := p.Endpoint()
			o.authURL, o.tokenURL, o.uiURL = or(o.authURL, ep.AuthURL), or(o.tokenURL, ep.TokenURL), or(o.uiURL, p.UserInfoEndpoint())
			o.verifier = p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}})
		}
		if o.discErr == nil && (o.authURL == "" || o.tokenURL == "" || o.uiURL == "") {
			o.discErr = errors.New("discovery document is missing an endpoint")
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
	var claims map[string]any
	subject := ""
	var idt *oidc.IDToken
	if o.oidc() {
		if tok.IDToken == "" {
			return nil, errors.New("the provider returned no id_token")
		}
		if idt, err = o.checkIDToken(ctx, tok.IDToken, a.Nonce); err != nil {
			return nil, fmt.Errorf("id_token refused: %w", err)
		}
		subject = idt.Subject
	}
	if err := o.getJSON(ctx, o.uiURL, tok.AccessToken, &claims); err != nil {
		return nil, err
	}
	if o.oidc() {
		if sub, _ := claims["sub"].(string); sub == "" || subtle.ConstantTimeCompare([]byte(sub), []byte(subject)) != 1 {
			return nil, errors.New("userinfo names a different subject than the id_token")
		}
	} else {
		// GitHub's stable key is its numeric user id; the login can change.
		id, ok := claims["id"].(float64)
		if !ok || id <= 0 {
			return nil, errors.New("the provider named no user id")
		}
		subject = fmt.Sprintf("%.0f", id)
	}
	emailVerified := verified(claims["email_verified"])
	if idt != nil && !emailVerified {
		var c struct {
			EmailVerified any `json:"email_verified"`
		}
		if idt.Claims(&c) == nil {
			emailVerified = verified(c.EmailVerified)
		}
	}
	name := ""
	for _, k := range []string{o.cfg.UsernameClaim, "preferred_username", "email", "login", "sub"} {
		if k == "" || (k == "email" && !emailVerified) {
			// An email the provider has not verified does not name anyone.
			continue
		}
		if v, ok := claims[k]; ok {
			if name = fmt.Sprint(v); name != "" {
				break
			}
		}
	}
	if name == "" {
		name = subject
	}
	var groups []string
	if gs, ok := claims[or(o.cfg.GroupsClaim, "groups")].([]any); ok {
		for _, g := range gs {
			groups = append(groups, fmt.Sprint(g))
		}
	}
	return &Identity{Provider: o.cfg.Name, Subject: subject, Username: name, Groups: groups}, nil
}

// checkIDToken verifies an id_token with go-oidc (signature, iss, aud, exp)
// and then what go-oidc leaves to the caller: iat present and not in the
// future, azp, and the nonce.
func (o *OAuth) checkIDToken(ctx context.Context, raw, nonce string) (*oidc.IDToken, error) {
	idt, err := o.verifier.Verify(oidc.ClientContext(ctx, o.http), raw)
	if err != nil {
		return nil, err
	}
	var c struct {
		Azp string          `json:"azp"`
		Exp json.RawMessage `json:"exp"`
		Iat json.RawMessage `json:"iat"`
	}
	if err := idt.Claims(&c); err != nil {
		return nil, err
	}
	if len(c.Exp) == 0 || string(c.Exp) == "null" || len(c.Iat) == 0 || string(c.Iat) == "null" {
		return nil, errors.New("exp and iat are required")
	}
	if idt.IssuedAt.After(time.Now().Add(idTokenSkew)) {
		return nil, errors.New("the token was issued in the future")
	}
	if c.Azp != "" && c.Azp != o.cfg.ClientID {
		return nil, errors.New("the token's authorized party is another client")
	}
	if len(idt.Audience) > 1 && c.Azp == "" {
		return nil, errors.New("the token names several audiences and no authorized party")
	}
	if idt.Nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("nonce does not match this sign-in")
	}
	return idt, nil
}

// verified reads an email_verified claim, which providers send as a boolean
// or as the string "true".
func verified(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
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
