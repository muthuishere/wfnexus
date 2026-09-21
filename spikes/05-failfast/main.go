// Spike 05 — resilience against the real wire: Retries / RetryBaseMs / TimeoutMs.
//
//	(a) a deliberately tiny TimeoutMs must abort the run LOUDLY
//	(b) a bad model name (HTTP 400) must fail FAST — no retries burned
//	(c) a 429/503 MUST be retried (httptest in front: cheap and deterministic)
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
)

var ctx = context.Background()

// counting transport: how many HTTP attempts the client actually made
type counter struct {
	n    int32
	base http.RoundTripper
}

func (c *counter) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.n, 1)
	b := c.base
	if b == nil {
		b = http.DefaultTransport
	}
	return b.RoundTrip(r)
}

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY unset")
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")
	tk, err := tn.CreateToolkit(ctx, tn.Options{Builtins: false})
	if err != nil {
		log.Fatal(err)
	}

	// ------------------------------------------------ (a) tiny TimeoutMs, live
	fmt.Println("== (a) TimeoutMs: 1ms whole-run deadline against the live wire ==")
	ca := &counter{}
	c := tn.CreateClient(tn.ClientOptions{
		BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, Model: model, APIKey: key,
		TimeoutMs: 1, Retries: 3, RetryBaseMs: 50,
		HTTPClient: &http.Client{Transport: ca},
	})
	t0 := time.Now()
	r, err := c.Run(ctx, "say hi", tk)
	fmt.Printf("elapsed : %s\n", time.Since(t0).Round(time.Millisecond))
	fmt.Printf("attempts: %d\n", atomic.LoadInt32(&ca.n))
	fmt.Printf("err     : %v\n", err)
	fmt.Printf("result  : status=%q text=%q\n", r.Status, r.Text)
	fmt.Println("  ^ LOUD? an error return, not a silent empty 'done'")

	// ------------------------------------------------ (b) bad model = 400, live
	fmt.Println("\n== (b) bad model name -> HTTP 400: must NOT be retried ==")
	cb := &counter{}
	c2 := tn.CreateClient(tn.ClientOptions{
		BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI,
		Model: "acme/definitely-not-a-model-v9", APIKey: key,
		Retries: 4, RetryBaseMs: 400, // 4 retries @400ms would take >6s if retried
		HTTPClient: &http.Client{Transport: cb},
	})
	t0 = time.Now()
	_, err = c2.Run(ctx, "say hi", tk)
	el := time.Since(t0)
	fmt.Printf("elapsed : %s\n", el.Round(time.Millisecond))
	fmt.Printf("attempts: %d  (1 == fail-fast; 5 == it burned the retry budget)\n", atomic.LoadInt32(&cb.n))
	fmt.Printf("err     : %v\n", err)

	// ------------------------------------------------ (c) 503 then 200, offline
	fmt.Println("\n== (c) 503 then 200 from an httptest origin: MUST be retried ==")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"upstream busy"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	c3 := tn.CreateClient(tn.ClientOptions{
		BaseURL: srv.URL, Style: tn.StyleOpenAI, Model: "stub", APIKey: "k",
		Retries: 3, RetryBaseMs: 50,
	})
	t0 = time.Now()
	r3, err := c3.Run(ctx, "say hi", tk)
	fmt.Printf("elapsed : %s   origin hits: %d\n", time.Since(t0).Round(time.Millisecond), atomic.LoadInt32(&hits))
	fmt.Printf("status  : %q text=%q err=%v\n", r3.Status, r3.Text, err)

	// (c2) exhausting the budget on a permanent 503
	fmt.Println("\n-- (c2) permanent 503: retried Retries times, then a loud error --")
	var hits2 int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits2, 1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":"always down"}`))
	}))
	defer srv2.Close()
	c4 := tn.CreateClient(tn.ClientOptions{
		BaseURL: srv2.URL, Style: tn.StyleOpenAI, Model: "stub", APIKey: "k",
		Retries: 2, RetryBaseMs: 30,
	})
	t0 = time.Now()
	_, err = c4.Run(ctx, "say hi", tk)
	fmt.Printf("elapsed : %s   origin hits: %d (Retries=2)\n", time.Since(t0).Round(time.Millisecond), atomic.LoadInt32(&hits2))
	fmt.Printf("err     : %v\n", err)

	// (c3) a 400 from the same httptest origin — the offline mirror of (b)
	fmt.Println("\n-- (c3) permanent 400 from the same shape of origin: must be 1 hit --")
	var hits3 int32
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits3, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"no such model"}}`))
	}))
	defer srv3.Close()
	c5 := tn.CreateClient(tn.ClientOptions{
		BaseURL: srv3.URL, Style: tn.StyleOpenAI, Model: "stub", APIKey: "k",
		Retries: 4, RetryBaseMs: 300,
	})
	t0 = time.Now()
	_, err = c5.Run(ctx, "say hi", tk)
	fmt.Printf("elapsed : %s   origin hits: %d\n", time.Since(t0).Round(time.Millisecond), atomic.LoadInt32(&hits3))
	fmt.Printf("err     : %v\n", err)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
