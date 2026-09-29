package ui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestAssetURLsCarryAContentVersion(t *testing.T) {
	for _, name := range []string{"app.css", "htmx.min.js", "play.js", "favicon.svg"} {
		url := Asset(name)
		if !regexp.MustCompile(`^/static/` + regexp.QuoteMeta(name) + `\?v=[0-9a-f]{8}$`).MatchString(url) {
			t.Errorf("Asset(%q) = %q", name, url)
		}
	}
	version := func(name string) string { return Asset(name)[strings.Index(Asset(name), "?v="):] }
	if version("app.css") == version("htmx.min.js") {
		t.Error("different files got the same version")
	}
}

func TestLayoutUsesVersionedAssets(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRenderer().Error(rec, http.StatusOK, "x")
	body := rec.Body.String()
	for _, name := range []string{"app.css", "htmx.min.js", "favicon.svg"} {
		if !strings.Contains(body, `"`+Asset(name)+`"`) {
			t.Errorf("layout does not reference %s", Asset(name))
		}
	}
}

func TestStaticCaching(t *testing.T) {
	cases := map[string]string{
		Asset("app.css"):         "public, max-age=31536000, immutable",
		"/static/app.css":        "no-cache",
		"/static/app.css?v=0000": "no-cache",
	}
	for url, want := range cases {
		rec := httptest.NewRecorder()
		Static().ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".symbol") {
			t.Errorf("%s: status %d", url, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", url, got, want)
		}
	}
}
