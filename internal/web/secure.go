package web

import (
	"net/http"
	"net/url"
)

// Endpoints called by other OAuth clients, possibly cross-origin. They don't
// use cookies, so cross-site requests can't abuse a session.
var crossOriginAllowed = map[string]bool{
	"/oauth/token":  true,
	"/oauth/revoke": true,
}

// Secure adds security headers and rejects cross-origin state-changing requests.
func Secure(baseURL string, next http.Handler) http.Handler {
	origin := baseURL
	if u, err := url.Parse(baseURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		// Never send URLs (the callback carries the auth code) to other sites.
		h.Set("Referrer-Policy", "same-origin")

		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !crossOriginAllowed[r.URL.Path] && !sameOrigin(r, origin) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin trusts Sec-Fetch-Site when the browser sends it (all current
// ones do); Origin alone is unreliable, e.g. it is "null" under some referrer
// policies. Requests with neither header come from non-browser clients.
func sameOrigin(r *http.Request, origin string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		o := r.Header.Get("Origin")
		return o == "" || o == origin
	default:
		return false
	}
}
