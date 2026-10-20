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

// Internal reports an address that is not on the public internet.
func Internal(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
		// 100.64/10 (carrier-grade NAT, also tailnets) and 0/8 are not
		// covered by the stdlib predicates.
		if ip4[0] == 0 || (ip4[0] == 100 && ip4[1]&0xc0 == 64) {
			return true
		}
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
