package engine

import "regexp"

// LLM transport errors arrive as "LLM <status>: <raw provider body>", and
// OpenRouter's 400 body carries the account's user_id. Run events are shown in
// the UI and stored, so provider identifiers are scrubbed before they land.
// Observed in spikes/05-failfast — see docs/spikes/FINDINGS.md.
var scrubPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)"?user_id"?\s*[:=]\s*"?[A-Za-z0-9_\-|]+"?`),
	regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9\-_]{16,}\b`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]{16,}=*`),
}

// scrub redacts provider account identifiers and anything key-shaped.
func scrub(s string) string {
	for _, re := range scrubPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}
