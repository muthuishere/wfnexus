package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

func runOf(t *testing.T, f hostedFixture, wf, tok string) (int, map[string]any) {
	t.Helper()
	return f.do(t, "POST", "/api/workflows/"+wf+"/runs", map[string]any{}, bearerHdr(tok))
}

// W4 done-when: over-quota gets a clear refusal — runs and spend.
func TestQuotasRefuseClearlyAndUsageIsReported(t *testing.T) {
	f, admin, acme, globex := provisionTwo(t)

	// A project cannot raise its own limits.
	if code, _ := f.do(t, "PUT", "/api/projects/acme/quotas", map[string]any{"runsPerDay": 1000}, bearerHdr(acme)); code != http.StatusForbidden {
		t.Fatalf("service token set its own quota: %d", code)
	}
	if code, out := f.do(t, "PUT", "/api/projects/acme/quotas", map[string]any{"runsPerDay": 2}, bearerHdr(admin)); code != 200 {
		t.Fatalf("set quota = %d %v", code, out)
	}
	if code, out := f.do(t, "GET", "/api/projects/acme/quotas", nil, bearerHdr(acme)); code != 200 || out["runsPerDay"] != float64(2) {
		t.Fatalf("read quota = %d %v", code, out)
	}
	for i := 0; i < 2; i++ {
		code, out := runOf(t, f, "acme%2Fdeploy", acme)
		if code != 201 {
			t.Fatalf("run %d = %d %v", i, code, out)
		}
		f.waitRun(t, out["run"].(map[string]any)["id"].(string))
	}
	// The third is refused: 429, Retry-After, and a body naming the limit.
	req := httptest.NewRequest("POST", "/api/workflows/acme%2Fdeploy/runs", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+acme)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over quota = %d (Retry-After %q) %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	_, out := runOf(t, f, "acme%2Fdeploy", acme)
	if out["code"] != "quota_runs_per_day" || out["limit"] != float64(2) || out["used"] != float64(2) {
		t.Fatalf("refusal body = %v", out)
	}
	// globex has its own quota (none).
	if code, out := runOf(t, f, "globex%2Fdeploy", globex); code != 201 {
		t.Fatalf("globex refused by acme's quota: %d %v", code, out)
	} else {
		f.waitRun(t, out["run"].(map[string]any)["id"].(string))
	}

	// Usage: today's row for acme says two runs, two steps.
	code, usage := f.do(t, "GET", "/api/projects/acme/usage", nil, bearerHdr(acme))
	days, _ := usage["days"].([]any)
	if code != 200 || len(days) != 1 {
		t.Fatalf("usage = %d %v", code, usage)
	}
	d := days[0].(map[string]any)
	if d["day"] != store.Day(time.Now()) || d["runs"] != float64(2) || d["steps"] != float64(2) {
		t.Fatalf("acme usage today = %v", d)
	}
	if code, _ := f.do(t, "GET", "/api/projects/globex/usage", nil, bearerHdr(acme)); code != http.StatusNotFound {
		t.Fatalf("acme read globex usage: %d", code)
	}

	// Spend: globex has spent $1.25 today against a $1 limit.
	if _, err := f.st.DB().Exec(`UPDATE project_usage SET cost_usd = 1.25 WHERE project = 'globex'`); err != nil {
		t.Fatal(err)
	}
	if code, out := f.do(t, "PUT", "/api/projects/globex/quotas", map[string]any{"costPerDayUsd": 1.0}, bearerHdr(admin)); code != 200 {
		t.Fatalf("set cost quota = %d %v", code, out)
	}
	code, out = runOf(t, f, "globex%2Fdeploy", globex)
	if code != http.StatusTooManyRequests || out["code"] != "quota_cost_per_day" || out["used"] != 1.25 {
		t.Fatalf("over spend = %d %v", code, out)
	}
}

// W4: rate limit per token, with a clear refusal; one token's limit is not
// another's.
func TestServiceTokenRateLimit(t *testing.T) {
	oldPer, oldBurst := serviceRatePerMin, serviceRateBurst
	serviceRatePerMin, serviceRateBurst = 1, 3
	t.Cleanup(func() { serviceRatePerMin, serviceRateBurst = oldPer, oldBurst })
	f, _, acme, globex := provisionTwo(t)
	for i := 0; i < 3; i++ {
		if code, _ := f.do(t, "GET", "/api/projects/acme", nil, bearerHdr(acme)); code != 200 {
			t.Fatalf("request %d within burst = %d", i, code)
		}
	}
	code, out := f.do(t, "GET", "/api/projects/acme", nil, bearerHdr(acme))
	if code != http.StatusTooManyRequests || out["code"] != "rate_limited" {
		t.Fatalf("over rate = %d %v", code, out)
	}
	if code, _ := f.do(t, "GET", "/api/projects/globex", nil, bearerHdr(globex)); code != 200 {
		t.Fatalf("globex limited by acme's traffic: %d", code)
	}
}
