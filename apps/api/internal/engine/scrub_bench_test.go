package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// benchSecrets is 50 secrets of a realistic length.
func benchSecrets() []secretValue {
	var out []secretValue
	for i := 0; i < 50; i++ {
		out = append(out, secretValue{fmt.Sprintf("KEY_%02d", i), fmt.Sprintf("s3cr3t-%02d-%s", i, strings.Repeat("abcdefgh", 4))})
	}
	return out
}

// benchDoc is a JSON array of 8,192 strings of about each bytes, three of them
// holding a secret.
func benchDoc(each int, secrets []secretValue) []byte {
	items := make([]string, 8192)
	pad := strings.Repeat("x", max(each-6, 1))
	for i := range items {
		items[i] = fmt.Sprintf("%04d%s", i, pad)
	}
	items[10] = "token " + secrets[0].value
	items[4000] = "token " + secrets[25].value
	items[8000] = "token " + secrets[49].value
	b, _ := json.Marshal(items)
	return b
}

func benchScrub(b *testing.B, each int) {
	secrets := benchSecrets()
	doc := benchDoc(each, secrets)
	b.SetBytes(int64(len(doc)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scrubJSON(doc, secrets)
	}
}

// 74 KB: 8,192 short strings.
func BenchmarkScrubJSON74KB(b *testing.B) { benchScrub(b, 9) }

// 1.2 MB: 8,192 strings of about 150 bytes.
func BenchmarkScrubJSON1200KB(b *testing.B) { benchScrub(b, 150) }

// A 1.2 MB step output with 50 secrets is scrubbed in well under a second even
// on a loaded machine (the target is far below; this bound only catches a
// return to a per-secret, per-form pass).
func TestScrubbingALargeOutputIsFast(t *testing.T) {
	secrets := benchSecrets()
	doc := benchDoc(150, secrets)
	_ = scrubJSON(doc, secrets) // warm: builds the redactor once
	start := time.Now()
	out := scrubJSON(doc, secrets)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("scrubbing %d KB took %v", len(doc)>>10, d)
	}
	for _, sv := range []secretValue{secrets[0], secrets[25], secrets[49]} {
		if strings.Contains(string(out), sv.value) {
			t.Fatalf("%s survived", sv.name)
		}
	}
}
