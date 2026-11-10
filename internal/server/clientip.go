package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// ParseTrustedProxies parses CIDRs (or bare IPs, as /32 or /128) of reverse
// proxies whose X-Forwarded-For is trusted. Blank entries are skipped. An
// invalid entry is an error naming it. A 4-in-6 prefix such as
// ::ffff:10.0.0.0/104 becomes the equivalent IPv4 prefix (10.0.0.0/8), since
// addresses are unmapped before matching.
func ParseTrustedProxies(cidrs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if p, err := netip.ParsePrefix(c); err == nil {
			if p.Addr().Is4In6() {
				if p.Bits() < 96 {
					return nil, fmt.Errorf("invalid trusted proxy %q: a 4-in-6 prefix needs at least /96", c)
				}
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(c)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: want a CIDR such as 10.0.0.0/8", c)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseHostAddr parses an address as it appears in RemoteAddr or an
// X-Forwarded-For entry: optional surrounding space, optional [...] and an
// optional :port ("1.2.3.4:5678", "[2001:db8::1]:443", "[2001:db8::1]",
// "2001:db8::1"). The result is unmapped, so ::ffff:a.b.c.d and a.b.c.d are
// the same key. "unknown" and anything else unparseable fail.
func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// clientIP returns the client address for rate limiting (R41, amended by
// R42). It is the immediate peer (RemoteAddr, normalised by parseHostAddr)
// unless that peer is a trusted proxy. Then X-Forwarded-For entries are
// walked right to left, each parsed only when reached: trusted entries are
// skipped and the first untrusted, parseable entry is the client. Entries to
// its left are client-controlled and never examined. The peer is returned
// when a malformed entry (or "unknown") is reached before any untrusted one,
// or when every entry is trusted or there are none. X-Forwarded-For from an
// untrusted peer is never read.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	pa, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return remoteIP(r.RemoteAddr)
	}
	peer := pa.String()
	if len(trusted) == 0 || !isTrusted(pa, trusted) {
		return peer
	}
	vals := r.Header.Values("X-Forwarded-For")
	for i := len(vals) - 1; i >= 0; i-- {
		entries := strings.Split(vals[i], ",")
		for j := len(entries) - 1; j >= 0; j-- {
			a, ok := parseHostAddr(entries[j])
			if !ok {
				return peer
			}
			if !isTrusted(a, trusted) {
				return a.String()
			}
		}
	}
	return peer
}

// publicURLInsecure reports whether publicURL is not https while listen is
// not a loopback address, so session cookies go out without Secure to a
// non-local audience.
func publicURLInsecure(publicURL, listen string) bool {
	if u, err := url.Parse(publicURL); err == nil && strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if host == "localhost" {
		return false
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err != nil || !a.IsLoopback()
}
