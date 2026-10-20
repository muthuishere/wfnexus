package notify

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"
)

// CheckAttachmentURL refuses an attachment URL an adapter must not fetch on
// the server's behalf (ADR 0023): anything but https, and any host that
// resolves to a loopback, private, link-local or otherwise internal address.
// Without it a `url` on `wfx send` is a server-side request forgery — the
// adapter fetches from inside the network and posts the bytes to a chat.
//
// It checks what the name resolves to NOW; the adapter re-checks at fetch
// time, which narrows (it cannot close) a DNS-rebinding window.
func CheckAttachmentURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("attachment url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("attachment url must be https, not %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("attachment url has no host")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("attachment url: %w", err)
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if Internal(ip) {
			return fmt.Errorf("attachment url %s resolves to an internal address (%s); refused", host, ip)
		}
	}
	return nil
}

// Internal reports an address that is not on the public internet. An IPv6
// address that CARRIES an IPv4 one — IPv4-mapped (::ffff:a.b.c.d),
// IPv4-compatible (::a.b.c.d), NAT64 (64:ff9b::/96) or 6to4 (2002::/16) — is
// judged by the IPv4 inside it, because that is where a packet to it ends up;
// Teredo (2001::/32) hides its IPv4 and the NAT64 local-use /48 is internal by
// definition, so both are refused outright.
func Internal(ip net.IP) bool {
	// To4 unwraps both a plain IPv4 and the IPv4-mapped ::ffff:a.b.c.d.
	if ip4 := ip.To4(); ip4 != nil {
		return internalV4(ip4)
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return true // not an address we can judge: refuse
	}
	zero := func(from, to int) bool {
		for _, b := range ip16[from:to] {
			if b != 0 {
				return false
			}
		}
		return true
	}
	switch {
	case ip16.IsUnspecified() || ip16.IsLoopback():
		return true
	case zero(0, 12): // ::a.b.c.d, IPv4-compatible
		return internalV4(net.IP(ip16[12:16]))
	case ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b: // 64:ff9b::/32
		if zero(4, 12) {
			return internalV4(net.IP(ip16[12:16])) // well-known NAT64 /96
		}
		return true // 64:ff9b:1::/48 local-use, and anything else in there
	case ip16[0] == 0x20 && ip16[1] == 0x02: // 6to4: 2002:a.b.c.d::/48
		return internalV4(net.IP(ip16[2:6]))
	case ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0 && ip16[3] == 0: // Teredo
		return true
	}
	return ip16.IsPrivate() || ip16.IsLinkLocalUnicast() || ip16.IsLinkLocalMulticast() ||
		ip16.IsInterfaceLocalMulticast() || ip16.IsMulticast()
}

func internalV4(ip4 net.IP) bool {
	ip4 = ip4.To4()
	if ip4 == nil {
		return true
	}
	// 100.64/10 (carrier-grade NAT, also tailnets) and 0/8 are not covered by
	// the stdlib predicates.
	if ip4[0] == 0 || (ip4[0] == 100 && ip4[1]&0xc0 == 64) {
		return true
	}
	return ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() ||
		ip4.IsMulticast() || ip4.IsUnspecified() || ip4.Equal(net.IPv4bcast)
}
