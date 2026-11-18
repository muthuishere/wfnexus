package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asciiJSON is s as python's json.dumps or jq --ascii-output writes it: every
// non-ASCII character as a \uXXXX escape.
func asciiJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteString(`\` + string(r))
		case r < 0x20 || r > 0x7e:
			if r > 0xffff {
				r -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// A step printed a JSON document with a secret in it, spelled however its tool
// escapes JSON. The stored output holds that document as a string. Every
// spelling is caught, because strings are decoded before they are scrubbed.
func TestASecretInPrintedJSONIsCaughtWhateverTheEscaping(t *testing.T) {
	slash := secretValue{"AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	accent := secretValue{"DB_PASSWORD", "pässwörd-é-ñ-日本-12345"}
	secrets := []secretValue{slash, accent}
	php := strings.ReplaceAll(`{"key":"`+slash.value+`"}`, "/", `\/`) // PHP json_encode default
	py := `{"password": ` + asciiJSON(accent.value) + `}`             // python json.dumps default
	for name, printed := range map[string]string{"php \\/": php, "python \\uXXXX": py} {
		t.Run(name, func(t *testing.T) {
			stored := scrubJSON(mustJSON(map[string]any{"stdout": printed + "\n"}), secrets)
			var back map[string]any
			_ = json.Unmarshal(stored, &back)
			text := fmt.Sprint(back["stdout"])
			for _, sv := range secrets {
				if strings.Contains(string(stored), sv.value) || strings.Contains(text, sv.value) {
					t.Fatalf("%s survived: %s", sv.name, stored)
				}
			}
			if !strings.Contains(text, "[redacted:") {
				t.Fatalf("nothing was redacted: %s", stored)
			}
			// The same text as an event.
			ev := redactEvent(map[string]any{"line": printed}, secrets)
			if s := string(mustJSON(ev)); !strings.Contains(s, "[redacted:") {
				t.Fatalf("event kept it: %s", s)
			}
		})
	}
}

// JSON quoted inside JSON inside JSON — three levels, each a string holding the
// next document, the inner one escaping / as PHP does — is decoded level by
// level and scrubbed.
func TestASecretThreeJSONLevelsDeepIsCaught(t *testing.T) {
	sv := secretValue{"API_TOKEN", "tok/abc/def/0123456789"}
	l3 := strings.ReplaceAll(string(mustJSON(map[string]any{"token": sv.value})), "/", `\/`)
	l2 := string(mustJSON(map[string]any{"body": l3}))
	l1 := string(mustJSON(map[string]any{"response": l2}))
	stored := scrubJSON(mustJSON(map[string]any{"stdout": l1}), []secretValue{sv})
	if !strings.Contains(string(stored), "[redacted:API_TOKEN]") {
		t.Fatalf("not redacted: %s", stored)
	}
	// Decode every level back: no level holds the value.
	var v any = string(stored)
	for i := 0; i < 6; i++ {
		s, ok := v.(string)
		if !ok {
			break
		}
		if strings.Contains(s, sv.value) || strings.Contains(s, strings.ReplaceAll(sv.value, "/", `\/`)) {
			t.Fatalf("level %d holds the value: %s", i, s)
		}
		var next any
		if json.Unmarshal([]byte(s), &next) != nil {
			break
		}
		b, _ := json.Marshal(next)
		v = string(b)
	}
}

// A step summary is scrubbed before it is cut to size, so a secret straddling
// the cut leaves no prefix behind.
func TestAStepSummaryIsScrubbedBeforeItIsCut(t *testing.T) {
	sv := secretValue{"API_TOKEN", "summary-secret-0123456789abcdef"}
	path := filepath.Join(t.TempDir(), "summary.md")
	// The secret starts 10 bytes before the cut.
	_ = os.WriteFile(path, []byte(strings.Repeat("a", 1000-10)+sv.value+strings.Repeat("b", 500)), 0o600)
	got := readCapped(path, 1000, []secretValue{sv})
	if strings.Contains(got, sv.value[:6]) || !strings.HasSuffix(got, summaryCutMarker) {
		t.Fatalf("tail = %q", got[max(len(got)-80, 0):])
	}
}

// Base64 wrapped at 76 columns (a PEM body, `base64` output) with the secret
// across a line break is caught, the span replaced, line breaks included.
func TestBase64WrappedAcrossLinesIsCaught(t *testing.T) {
	sv := secretValue{"SIGNING_KEY", strings.Repeat("k3y-material-", 8)}
	enc := base64.StdEncoding.EncodeToString([]byte("prefix bytes " + sv.value + " suffix"))
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 76 {
		wrapped.WriteString(enc[i:min(i+76, len(enc))] + "\n")
	}
	got := redactText("-----BEGIN DATA-----\n"+wrapped.String()+"-----END DATA-----\n", []secretValue{sv})
	if !strings.Contains(got, "[redacted:SIGNING_KEY]") {
		t.Fatalf("not caught:\n%s", got)
	}
	flat := strings.ReplaceAll(got, "\n", "")
	for _, f := range base64Forms([]byte(sv.value)) {
		if len(f) >= 16 && strings.Contains(flat, f) {
			t.Fatalf("a base64 form survived:\n%s", got)
		}
	}
}
