package engine

import "net/netip"

// TrustedProxies are the reverse proxies whose X-Forwarded-For is believed.
func (e *Engine) TrustedProxies() []netip.Prefix { return e.cfg.TrustedProxies }
