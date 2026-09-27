// Package clientip finds the visitor's IP address when requests arrive through
// Railway's edge proxy and, optionally, Cloudflare.
//
// Railway rebuilds X-Forwarded-For at its edge (clients can't forge it), so its
// first entry is whoever connected to Railway: the visitor, or a Cloudflare
// server. Only in the second case is Cloudflare's CF-Connecting-IP trusted;
// otherwise anyone skipping Cloudflare could send a made-up one.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Resolver struct {
	// TrustProxy: all traffic comes through a proxy that rebuilds
	// X-Forwarded-For (Railway). Never set it when clients can reach the app
	// directly, or they could pick their own IP.
	TrustProxy bool
}

// IP returns the visitor's IP address.
func (res Resolver) IP(r *http.Request) netip.Addr {
	peer := remoteAddr(r)
	if res.TrustProxy {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if a, ok := parse(first); ok {
			peer = a
		}
	}
	if isCloudflare(peer) {
		if a, ok := parse(r.Header.Get("CF-Connecting-IP")); ok {
			return a
		}
	}
	return peer
}

// Middleware sets r.RemoteAddr to the visitor's IP, so handlers further down
// (login rate limiting, logs) see the visitor rather than a proxy.
func (res Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := res.IP(r); ip.IsValid() {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		}
		next.ServeHTTP(w, r)
	})
}

func remoteAddr(r *http.Request) netip.Addr {
	a, _ := parse(r.RemoteAddr)
	return a
}

// parse accepts an address with or without a port.
func parse(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}

func isCloudflare(a netip.Addr) bool {
	for _, p := range cloudflareRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// cloudflareRanges are Cloudflare's published addresses, from
// https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6
// (fetched 2026-09-27). They change rarely.
var cloudflareRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
		"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()
