package config

import "os"

func tokenIssuerFromEnv() TokenIssuer {
	return TokenIssuer{
		Issuer:   os.Getenv("WFX_TOKEN_ISSUER"),
		Audience: os.Getenv("WFX_TOKEN_AUDIENCE"),
		JWKSURL:  os.Getenv("WFX_TOKEN_JWKS_URL"),
	}
}

// TokenIssuer configures acceptance of an external issuer's access tokens.
// Empty means none are accepted.
type TokenIssuer struct {
	Issuer   string
	Audience string
	JWKSURL  string
}

// Enabled reports whether all three settings are present.
func (t TokenIssuer) Enabled() bool { return t.Issuer != "" && t.Audience != "" && t.JWKSURL != "" }

// Partial reports a configuration with some but not all settings, which boot
// refuses rather than guessing.
func (t TokenIssuer) Partial() bool {
	n := 0
	for _, v := range []string{t.Issuer, t.Audience, t.JWKSURL} {
		if v != "" {
			n++
		}
	}
	return n > 0 && n < 3
}
