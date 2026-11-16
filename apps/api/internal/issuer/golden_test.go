package issuer

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// The golden vectors are tokens minted by the issuer's own reference
// implementation (contract ContractVersion) with a TEST key, committed as a
// static file. Each carries the verdict that implementation gives; this
// verifier must give the same one. See testdata/README.md for the source.
type goldenFile struct {
	Contract string          `json:"contract"`
	Issuer   string          `json:"issuer"`
	Audience string          `json:"audience"`
	VerifyAt time.Time       `json:"verify_at"`
	MaxSkew  int             `json:"max_clock_skew_seconds"`
	JWKS     json.RawMessage `json:"jwks"`
	Vectors  []struct {
		Name    string `json:"name"`
		Token   string `json:"token"`
		Verdict string `json:"verdict"`
	} `json:"vectors"`
}

// The reference implementation's reason classes, mapped to ours.
var goldenVerdicts = map[string]error{
	"valid":                 nil,
	"missing_claim":         ErrMissingClaim,
	"wrong_audience":        ErrAudience,
	"wrong_issuer":          ErrIssuer,
	"expired":               ErrExpired,
	"algorithm_not_allowed": ErrAlgorithm,
	"unknown_key":           ErrUnknownKey,
	"bad_signature":         ErrSignature,
}

func TestGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/token-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g.Contract != ContractVersion {
		t.Fatalf("fixture contract %q, verifier implements %q: regenerate the fixture or update the suite", g.Contract, ContractVersion)
	}
	keys, err := ParseJWKS(g.JWKS)
	if err != nil {
		t.Fatal(err)
	}
	v := &Verifier{
		Issuer: g.Issuer, Audience: g.Audience,
		Keys:    func(string) (KeySet, error) { return keys, nil },
		MaxSkew: time.Duration(g.MaxSkew) * time.Second,
		Now:     func() time.Time { return g.VerifyAt },
	}
	if len(g.Vectors) < 8 {
		t.Fatalf("only %d vectors", len(g.Vectors))
	}
	valid := 0
	for _, vec := range g.Vectors {
		want, known := goldenVerdicts[vec.Verdict]
		if !known {
			t.Fatalf("%s: verdict %q has no mapping; the contract changed, update this suite", vec.Name, vec.Verdict)
		}
		c, err := v.Verify(vec.Token)
		if want == nil {
			valid++
			if err != nil {
				t.Errorf("%s: refused a valid token: %v", vec.Name, err)
				continue
			}
			if c.Tenant == "" || c.Subject == "" || c.Session == "" || c.AuthTime.IsZero() {
				t.Errorf("%s: claims incomplete: %+v", vec.Name, c)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", vec.Name, err, want)
		}
	}
	if valid == 0 {
		t.Fatal("no valid vector: the suite would pass a verifier that refuses everything")
	}
}
