package clientip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const cloudflareIP = "173.245.48.5" // inside 173.245.48.0/20

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
		{"behind the proxy, first forwarded address wins", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.9, 10.0.0.3"}, "198.51.100.9"},
		{"through Cloudflare, Cloudflare's header is used", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": cloudflareIP, "CF-Connecting-IP": "198.51.100.9"}, "198.51.100.9"},
		{"skipping Cloudflare, a fake Cloudflare header is ignored", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "203.0.113.66", "CF-Connecting-IP": "198.51.100.9"}, "203.0.113.66"},
		{"Cloudflare straight in front of the app", false, cloudflareIP + ":443",
			map[string]string{"CF-Connecting-IP": "198.51.100.9"}, "198.51.100.9"},
		{"Cloudflare over IPv6", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "2606:4700::1", "CF-Connecting-IP": "2001:db8::42"}, "2001:db8::42"},
		{"IPv4-mapped IPv6 is unwrapped", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "::ffff:198.51.100.9"}, "198.51.100.9"},
		{"forwarded address with a port", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.9:4711"}, "198.51.100.9"},
		{"unparsable forwarded address falls back to the connection", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": "unknown"}, "10.0.0.2"},
		{"unparsable Cloudflare header falls back to Cloudflare's address", true, "10.0.0.2:5555",
			map[string]string{"X-Forwarded-For": cloudflareIP, "CF-Connecting-IP": "nope"}, cloudflareIP},
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
	r.Header.Set("X-Forwarded-For", "2001:db8::42")
	h.ServeHTTP(httptest.NewRecorder(), r)
	host, _, err := net.SplitHostPort(seen)
	if err != nil || host != "2001:db8::42" {
		t.Errorf("handler saw RemoteAddr %q", seen)
	}
}

func TestCloudflareRanges(t *testing.T) {
	for ip, want := range map[string]bool{
		"173.245.48.5": true, "104.16.0.1": true, "172.71.255.255": true, "2a06:98c7::1": true,
		"8.8.8.8": false, "172.72.0.1": false, "2001:db8::1": false,
	} {
		if got := isCloudflare(mustAddr(t, ip)); got != want {
			t.Errorf("isCloudflare(%s) = %v, want %v", ip, got, want)
		}
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
