package clientip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tunnel is cloudflared's address on Railway's private network.
const tunnel = "[fd12:3456::5]:5555"

func TestIP(t *testing.T) {
	cases := []struct {
		name       string
		trustProxy bool
		remote     string
		headers    map[string]string
		want       string
	}{
		{"direct, no headers", false, "203.0.113.7:5555", nil, "203.0.113.7"},
		{"direct, forwarding headers are ignored", false, "203.0.113.7:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.9", "CF-Connecting-IP": "198.51.100.9"}, "203.0.113.7"},
		{"through the tunnel, Cloudflare's header is used", true, tunnel,
			map[string]string{"CF-Connecting-IP": "198.51.100.9"}, "198.51.100.9"},
		{"a made-up X-Forwarded-For is ignored", true, tunnel,
			map[string]string{"X-Forwarded-For": "203.0.113.66, 198.51.100.9", "CF-Connecting-IP": "198.51.100.9"}, "198.51.100.9"},
		{"X-Forwarded-For alone is not trusted", true, tunnel,
			map[string]string{"X-Forwarded-For": "203.0.113.66"}, "fd12:3456::5"},
		{"IPv6 visitor", true, tunnel, map[string]string{"CF-Connecting-IP": "2001:db8::42"}, "2001:db8::42"},
		{"IPv4-mapped IPv6 is unwrapped", true, tunnel,
			map[string]string{"CF-Connecting-IP": "::ffff:198.51.100.9"}, "198.51.100.9"},
		{"unparsable header falls back to the connection", true, tunnel,
			map[string]string{"CF-Connecting-IP": "nope"}, "fd12:3456::5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := (Resolver{TrustProxy: c.trustProxy}).IP(r); got.String() != c.want {
				t.Errorf("IP = %s, want %s", got, c.want)
			}
		})
	}
}

func TestMiddlewareRewritesRemoteAddr(t *testing.T) {
	var seen string
	h := Resolver{TrustProxy: true}.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-Connecting-IP", "2001:db8::42")
	h.ServeHTTP(httptest.NewRecorder(), r)
	host, _, err := net.SplitHostPort(seen)
	if err != nil || host != "2001:db8::42" {
		t.Errorf("handler saw RemoteAddr %q", seen)
	}
}
