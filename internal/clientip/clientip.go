// Package clientip finds the visitor's IP address when requests arrive through
// a Cloudflare Tunnel.
//
// In production the app has no public address: cloudflared, a separate Railway
// service, reaches it over Railway's private network, so every request has come
// through Cloudflare, which puts the visitor's address in CF-Connecting-IP.
// X-Forwarded-For is not used: Cloudflare adds to whatever the visitor sent, so
// its first entry can be made up.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Resolver struct {
	// TrustProxy: all traffic comes through Cloudflare (the tunnel). Never set
	// it when clients can reach the app another way, or they could pick their own IP.
	TrustProxy bool
}

// IP returns the visitor's IP address.
func (res Resolver) IP(r *http.Request) netip.Addr {
	if res.TrustProxy {
		if a, ok := parse(r.Header.Get("CF-Connecting-IP")); ok {
			return a
		}
	}
	return remoteAddr(r)
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

// Of returns the visitor's IP address as text, once Middleware has run.
func Of(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
