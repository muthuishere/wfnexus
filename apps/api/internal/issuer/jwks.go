package issuer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// JWKS fetches and caches an issuer's key set.
//
//   - The URL is https, or http to a loopback host (a local issuer in
//     development).
//   - A fetched set is used for TTL. A kid that is not in the cached set
//     triggers ONE refetch, at most every MinRefetch, so a stream of tokens
//     with made-up kids cannot turn into a stream of requests to the issuer.
//   - A failed refetch keeps serving the last good set until it is older
//     than MaxStale; then verification fails closed.
type JWKS struct {
	URL        string
	Client     *http.Client
	TTL        time.Duration
	MinRefetch time.Duration
	MaxStale   time.Duration
	Now        func() time.Time

	mu      sync.Mutex
	keys    KeySet
	fetched time.Time
	tried   time.Time
}

const maxJWKSBytes = 64 << 10

// CheckJWKSURL refuses a URL that is not https (or http to loopback).
func CheckJWKSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("jwks url must be an absolute URL without credentials")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return errors.New("jwks url must be https (http only to a loopback host)")
}

func (j *JWKS) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *JWKS) durations() (ttl, min, stale time.Duration) {
	ttl, min, stale = j.TTL, j.MinRefetch, j.MaxStale
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if min <= 0 {
		min = 30 * time.Second
	}
	if stale <= 0 {
		stale = time.Hour
	}
	return
}

// KeysFor returns the cached set, refetching when it is older than TTL or
// (rate-limited) when kid is not in it. It is a Verifier's Keys.
func (j *JWKS) KeysFor(kid string) (KeySet, error) {
	j.mu.Lock()
	ttl, min, stale := j.durations()
	now := j.now()
	fresh := j.keys != nil && now.Sub(j.fetched) < ttl
	missing := kid != "" && j.keys != nil && j.keys[kid] == nil
	if fresh && !missing {
		ks := j.keys
		j.mu.Unlock()
		return ks, nil
	}
	// Rate-limited ALSO before the first success: an unreachable issuer at
	// boot must not turn every presented token into a fetch. The fetch runs
	// WITHOUT the lock, so a slow issuer never queues other requests behind
	// it; they are answered from what is cached meanwhile.
	doFetch := j.tried.IsZero() || now.Sub(j.tried) >= min
	if doFetch {
		j.tried = now
	}
	j.mu.Unlock()
	if doFetch {
		if ks, err := j.fetch(); err == nil {
			j.mu.Lock()
			j.keys, j.fetched = ks, now
			j.mu.Unlock()
			return ks, nil
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.keys != nil && j.now().Sub(j.fetched) < stale {
		return j.keys, nil
	}
	return nil, errors.New("key set unavailable")
}

func (j *JWKS) fetch() (KeySet, error) {
	if err := CheckJWKSURL(j.URL); err != nil {
		return nil, err
	}
	c := j.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	// No redirects: the key set comes from the configured URL or not at all.
	cc := *c
	cc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c = &cc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: %s", res.Status)
	}
	doc, err := io.ReadAll(io.LimitReader(res.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(doc) > maxJWKSBytes {
		return nil, errors.New("jwks: too large")
	}
	return ParseJWKS(doc)
}
