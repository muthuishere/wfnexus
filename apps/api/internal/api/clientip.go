package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientIP decides which address a request comes from, once, for everything
// after it: the request log and every per-address limit (sign-in, the device
// grant's verification, the exposed hooks).
//
// The direct peer is the client unless it is one of the configured trusted
// proxies. Only then is X-Forwarded-For read, from the right: each trusted hop
// is skipped and the first address that is not one is the client. Everything
// to the left of it was written by whoever that client is, so it is not read.
// True-Client-IP and X-Real-IP are never read: nothing here says who set them.
//
// With no trusted proxies (the default) the request is left as it arrived, so
// a forwarded header changes nothing whoever sends it. Behind a proxy that is
// not configured, every client shares the proxy's address; configuring it is
// what separates them.
func clientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(trusted) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, ok := forwardedClient(r, trusted); ok {
				r.RemoteAddr = netip.AddrPortFrom(c, 0).String()
			}
			next.ServeHTTP(w, r)
		})
	}
}

// forwardedClient is the client a trusted peer forwarded for, or false when
// the peer is not trusted or the header names no usable client.
func forwardedClient(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, ok := parseHop(r.RemoteAddr)
	if !ok || !isTrusted(peer, trusted) {
		return netip.Addr{}, false
	}
	// Every header line, in order, is one list: a proxy may append a line
	// rather than extend the last one.
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			// A hop that is not an address breaks the chain: what lies
			// beyond it cannot be attributed, so the peer stands.
			return netip.Addr{}, false
		}
		if !isTrusted(a, trusted) {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// parseHop reads an address, with or without a port, v4-mapped folded to v4.
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// proxyWarning is said once at startup when a server reachable from other
// machines trusts no proxy: correct when nothing sits in front of it, and the
// reason every client shares one sign-in limit when something does.
func proxyWarning(loopbackOnly bool, trusted []netip.Prefix) string {
	if loopbackOnly || len(trusted) > 0 {
		return ""
	}
	return "WARNING behind a reverse proxy, set trustedProxies (WFX_TRUSTED_PROXIES) or every client shares one rate-limit address"
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
