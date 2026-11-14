package engine

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

// Every form a step might print a secret in is removed: raw, JSON-escaped,
// base64 (both alphabets, padded or not), hex (both cases), URL-encoded.
func TestRedactTextRemovesEveryFormOfASecret(t *testing.T) {
	v := "s3cr/t+v=lue&x?"
	secrets := []secretValue{{"K", v}}
	b := []byte(v)
	for _, f := range []string{v, base64.StdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
		hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)), url.QueryEscape(v), url.PathEscape(v)} {
		if got := redactText("before "+f+" after", secrets); strings.Contains(got, f) || !strings.Contains(got, "[redacted:K]") {
			t.Errorf("form %q survived: %q", f, got)
		}
	}
	out := scrubOutput(map[string]any{"stdout": "x " + v + " y", "n": 3}, secrets)
	if strings.Contains(out["stdout"].(string), v) || out["n"] != float64(3) {
		t.Fatalf("output = %v", out)
	}
	if got := scrubJSON([]byte(`{"a":"`+hex.EncodeToString(b)+`"}`), secrets); strings.Contains(string(got), hex.EncodeToString(b)) {
		t.Fatalf("json = %s", got)
	}
}
