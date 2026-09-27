package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danilopavk/alphabeter/internal/app"
	"github.com/danilopavk/alphabeter/internal/game"
	"github.com/danilopavk/alphabeter/internal/testutil"
)

type env struct {
	t    *testing.T
	base string
	app  *app.App
	pool *pgxpool.Pool
}

func start(t *testing.T, options ...func(*app.Config)) *env {
	pool := testutil.Postgres(t)
	rdb, prefix := testutil.Redis(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	cfg := app.Config{BaseURL: base, KeyPrefix: prefix}
	for _, option := range options {
		option(&cfg)
	}
	a := app.New(cfg, pool, rdb)
	srv := httptest.NewUnstartedServer(a.Handler)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &env{t: t, base: base, app: a, pool: pool}
}

// browser is a cookie-keeping client. follow decides whether it follows redirects.
type browser struct {
	env    *env
	jar    *cookiejar.Jar
	follow *http.Client
	single *http.Client
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{
		env:    e,
		jar:    jar,
		follow: &http.Client{Jar: jar},
		single: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

type response struct {
	*http.Response
	body string
}

func (b *browser) do(client *http.Client, method, path string, form url.Values, headers ...string) response {
	b.env.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.env.base+path, body)
	if err != nil {
		b.env.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		b.env.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return response{resp, string(raw)}
}

func (b *browser) get(path string, headers ...string) response {
	return b.do(b.follow, "GET", path, nil, headers...)
}

func (b *browser) post(path string, form url.Values, headers ...string) response {
	return b.do(b.follow, "POST", path, form, headers...)
}

func (b *browser) cookie(name string) string {
	u, _ := url.Parse(b.env.base)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// signUp registers through the real login redirect chain and lands on /kids.
func (b *browser) signUp(email string) {
	t := b.env.t
	t.Helper()
	page := b.get("/kids")
	if page.Request.URL.Path != "/oauth/authorize" {
		t.Fatalf("unauthenticated /kids ended at %s", page.Request.URL)
	}
	form := page.Request.URL.Query()
	form.Set("email", email)
	form.Set("password", "correct horse")
	kids := b.post("/oauth/register", form)
	if kids.StatusCode != http.StatusOK || kids.Request.URL.Path != "/kids" {
		t.Fatalf("sign-up ended at %s with %d: %s", kids.Request.URL, kids.StatusCode, kids.body)
	}
}

var kidIDPattern = regexp.MustCompile(`/kids/(\d+)(?:-[^/"]*)?/play`)

func (b *browser) addKid(name string) string {
	t := b.env.t
	t.Helper()
	res := b.post("/kids", url.Values{"name": {name}}, "HX-Request", "true")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.body, name) {
		t.Fatalf("add kid: %d %s", res.StatusCode, res.body)
	}
	ids := kidIDPattern.FindAllStringSubmatch(res.body, -1)
	return ids[len(ids)-1][1]
}

func TestLoginRedirectStartsPKCEFlow(t *testing.T) {
	e := start(t)
	b := e.browser()

	res := b.do(b.single, "GET", "/kids", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("/kids: %d → %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = b.do(b.single, "GET", "/login", nil)
	loc, _ := url.Parse(res.Header.Get("Location"))
	q := loc.Query()
	if loc.Path != "/oauth/authorize" || q.Get("client_id") != "web" || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != e.base+"/auth/callback" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("code_challenge")) != 43 || q.Get("state") == "" {
		t.Errorf("authorize URL %s", loc)
	}
	var tx *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "oauth_tx" {
			tx = c
		}
	}
	if tx == nil || !tx.HttpOnly || tx.Path != "/auth/callback" {
		t.Fatalf("oauth_tx cookie = %+v", tx)
	}
	state, verifier, _ := strings.Cut(tx.Value, ".")
	sum := sha256.Sum256([]byte(verifier))
	if state != q.Get("state") || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("cookie does not match the state and challenge sent")
	}
}

func TestSignUpSetsSessionCookies(t *testing.T) {
	e := start(t)
	b := e.browser()
	page := b.get("/kids")
	form := page.Request.URL.Query()
	form.Set("email", "parent@example.com")
	form.Set("password", "correct horse")

	res := b.do(b.single, "POST", "/oauth/register", form) // → 302 to /auth/callback
	res = b.do(b.single, "GET", strings.TrimPrefix(res.Header.Get("Location"), e.base), nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/kids" {
		t.Fatalf("callback: %d → %s: %s", res.StatusCode, res.Header.Get("Location"), res.body)
	}
	got := map[string]*http.Cookie{}
	for _, c := range res.Cookies() {
		got[c.Name] = c
	}
	for _, name := range []string{"at", "rt"} {
		c := got[name]
		if c == nil || c.Value == "" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Errorf("cookie %s = %+v", name, c)
		}
	}
	if c := got["oauth_tx"]; c == nil || c.MaxAge >= 0 {
		t.Errorf("oauth_tx not cleared: %+v", c)
	}
	if res := b.get("/kids"); res.StatusCode != http.StatusOK || !strings.Contains(res.body, "Add a kid") {
		t.Errorf("/kids after login: %d", res.StatusCode)
	}
}

func TestCallbackRejectsWrongState(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.do(b.single, "GET", "/login", nil)
	res := b.do(b.single, "GET", "/auth/callback?code=x&state=wrong", nil)
	if res.StatusCode != http.StatusBadRequest || b.cookie("at") != "" {
		t.Errorf("status %d, at cookie %q", res.StatusCode, b.cookie("at"))
	}
	res = b.do(b.single, "GET", "/auth/callback?error=access_denied", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("error callback status %d", res.StatusCode)
	}
}

func TestExpiredAccessTokenRefreshesSilently(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	oldAT, oldRT := b.cookie("at"), b.cookie("rt")
	e.app.IDP.Revoke(context.Background(), oldAT) // as if the access token expired in Redis

	if res := b.get("/kids"); res.StatusCode != http.StatusOK || res.Request.URL.Path != "/kids" {
		t.Fatalf("/kids: %d at %s", res.StatusCode, res.Request.URL)
	}
	if b.cookie("at") == oldAT || b.cookie("rt") == oldRT {
		t.Error("tokens were not rotated")
	}
}

func TestUnauthenticatedHtmxRequestGetsHXRedirect(t *testing.T) {
	e := start(t)
	b := e.browser()
	res := b.do(b.single, "POST", "/kids", url.Values{"name": {"Mia"}}, "HX-Request", "true")
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get("HX-Redirect") != "/login" {
		t.Errorf("status %d, HX-Redirect %q", res.StatusCode, res.Header.Get("HX-Redirect"))
	}
}

func TestPlay(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	id := b.addKid("Mia")

	page := b.get("/kids/" + id + "/play")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.body, `name="seq" value="0"`) {
		t.Fatalf("play page: %d %s", page.StatusCode, page.body)
	}
	symbol := regexp.MustCompile(`<div class="symbol"[^>]*>(.)</div>`).FindStringSubmatch(page.body)
	if symbol == nil {
		t.Fatal("no symbol on the play page")
	}
	if s, ok := game.ParseSymbol(symbol[1]); !ok || !slices.Contains(game.Batches[0], s) {
		t.Errorf("first symbol %q is not from batch 1", symbol[1])
	}
	for _, want := range []string{
		`<div class="score-value">` + symbol[1] + ` score: 100</div>`,
		`<div class="score-note">Over 130 needed for the next level</div>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("play page lacks %s", want)
		}
	}
	if strings.Contains(page.body, `class="flash`) {
		t.Error("a fresh play page shows an answer flash")
	}

	answer := func(seq string) response {
		return b.post("/kids/"+id+"/answer", url.Values{"seq": {seq}, "correct": {"1"}}, "HX-Request", "true")
	}
	res := answer("0")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(res.body), `<main id="stage"`) ||
		!strings.Contains(res.body, `name="seq" value="1"`) {
		t.Fatalf("answer fragment: %d %s", res.StatusCode, res.body)
	}
	flash := regexp.MustCompile(`<div class="flash right"[^>]*>\s*<span class="flash-symbol">(.)</span>[^<]*` +
		`<span class="flash-score">100.5</span>\s*<span class="flash-delta">(?:\+|&#43;)0.5</span>`).FindStringSubmatch(res.body)
	if flash == nil || flash[1] != symbol[1] {
		t.Errorf("answer fragment lacks the score flash for %s: %s", symbol[1], res.body)
	}
	if res = answer("0"); !strings.Contains(res.body, `name="seq" value="1"`) { // double tap
		t.Errorf("double tap fragment: %s", res.body)
	}
	if strings.Contains(res.body, `class="flash`) {
		t.Error("a rejected double tap shows a score flash")
	}
	var seq int
	e.pool.QueryRow(context.Background(), `SELECT seq FROM kids WHERE id = $1`, id).Scan(&seq)
	if seq != 1 {
		t.Errorf("seq in DB = %d, want 1", seq)
	}

	// Without htmx the form still works.
	res = b.post("/kids/"+id+"/answer", url.Values{"seq": {"1"}, "correct": {"0"}})
	if res.Request.URL.Path != "/kids/"+id+"-mia/play" || !strings.Contains(res.body, `name="seq" value="2"`) {
		t.Errorf("plain form post ended at %s", res.Request.URL)
	}
}

func TestKidURLsContainTheName(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	id := b.addKid("Ana Marija")
	canonical := "/kids/" + id + "-ana-marija/play"

	if list := b.get("/kids"); !strings.Contains(list.body, `href="`+canonical+`"`) {
		t.Errorf("kids list does not link to %s", canonical)
	}
	for _, old := range []string{"/kids/" + id + "/play", "/kids/" + id + "-someone-else/play"} {
		res := b.do(b.single, "GET", old, nil)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != canonical {
			t.Errorf("GET %s: %d → %q, want redirect to %s", old, res.StatusCode, res.Header.Get("Location"), canonical)
		}
	}
	page := b.get(canonical)
	if page.StatusCode != http.StatusOK || page.Request.URL.Path != canonical ||
		!strings.Contains(page.body, `hx-post="/kids/`+id+`-ana-marija/answer"`) {
		t.Errorf("play page at %s: %d", page.Request.URL.Path, page.StatusCode)
	}
}

func TestKidsBelongToTheirParent(t *testing.T) {
	e := start(t)
	owner := e.browser()
	owner.signUp("parent@example.com")
	id := owner.addKid("Mia")

	other := e.browser()
	other.signUp("other@example.com")
	if res := other.get("/kids/" + id + "/play"); res.StatusCode != http.StatusNotFound {
		t.Errorf("play: %d", res.StatusCode)
	}
	if res := other.post("/kids/"+id+"/answer", url.Values{"seq": {"0"}, "correct": {"1"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("answer: %d", res.StatusCode)
	}
	if res := other.post("/kids/"+id+"/delete", nil, "HX-Request", "true"); res.StatusCode != http.StatusNotFound {
		t.Errorf("delete: %d", res.StatusCode)
	}
	if res := other.get("/kids"); strings.Contains(res.body, "Mia") {
		t.Error("other parent sees Mia")
	}
}

func TestAddKidValidationAndDelete(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")

	res := b.post("/kids", url.Values{"name": {"  "}}, "HX-Request", "true")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.body, "1 to 40 characters") {
		t.Errorf("empty name: %d %s", res.StatusCode, res.body)
	}
	id := b.addKid("Mia")
	res = b.post("/kids/"+id+"/delete", nil, "HX-Request", "true")
	if res.StatusCode != http.StatusOK || strings.Contains(res.body, "Mia") {
		t.Errorf("after delete: %d %s", res.StatusCode, res.body)
	}
}

func TestLogout(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	rt := b.cookie("rt")

	b.post("/logout", nil)
	if b.cookie("at") != "" || b.cookie("rt") != "" {
		t.Error("session cookies survive logout")
	}
	if _, err := e.app.IDP.Refresh(context.Background(), "web", rt); err == nil {
		t.Error("refresh token still valid after logout")
	}
	if res := b.do(b.single, "GET", "/kids", nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("/kids after logout: %d", res.StatusCode)
	}
}

func TestCrossOriginPostsAreRejected(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")

	res := b.post("/kids", url.Values{"name": {"Mia"}}, "Origin", "http://evil.test")
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST /kids: %d", res.StatusCode)
	}
	res = b.post("/kids", url.Values{"name": {"Mia"}}, "Sec-Fetch-Site", "cross-site")
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST /kids: %d", res.StatusCode)
	}
	if res = b.post("/kids", url.Values{"name": {"Mia"}}, "Origin", e.base, "HX-Request", "true"); res.StatusCode != http.StatusOK {
		t.Errorf("same-origin POST /kids: %d", res.StatusCode)
	}
	// What Chrome sends for a same-origin form post under a no-referrer policy.
	res = b.post("/kids", url.Values{"name": {"Mia"}}, "Origin", "null", "Sec-Fetch-Site", "same-origin", "HX-Request", "true")
	if res.StatusCode != http.StatusOK {
		t.Errorf("same-origin POST with Origin: null: %d", res.StatusCode)
	}
	res = b.post("/kids", url.Values{"name": {"Mia"}}, "Origin", e.base, "Sec-Fetch-Site", "same-site")
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("same-site (other subdomain) POST /kids: %d", res.StatusCode)
	}
	// The token endpoint is for other clients and must stay reachable cross-origin.
	res = b.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {"nope"}}, "Origin", "http://evil.test")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("cross-origin token request: %d", res.StatusCode)
	}
}

func TestLoginRateLimitUsesTheRealClientIP(t *testing.T) {
	e := start(t, func(c *app.Config) { c.TrustProxy = true })
	b := e.browser()
	attempt := func(ip string, n int) int {
		form := url.Values{
			"response_type": {"code"}, "client_id": {"web"}, "redirect_uri": {e.base + "/auth/callback"},
			"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"},
			"email": {fmt.Sprintf("guess%d@example.com", n)}, "password": {"wrong password"},
		}
		return b.do(b.single, "POST", "/oauth/authorize", form, "X-Forwarded-For", ip).StatusCode
	}
	for n := range 10 {
		if status := attempt("198.51.100.1", n); status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", n, status)
		}
	}
	if status := attempt("198.51.100.1", 10); status != http.StatusTooManyRequests {
		t.Errorf("11th attempt from the same visitor: status %d, want 429", status)
	}
	if status := attempt("198.51.100.2", 11); status != http.StatusUnauthorized {
		t.Errorf("another visitor behind the same proxy: status %d, want 401", status)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := start(t)
	res := e.browser().get("/login")
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
	} {
		if got := res.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if res := e.browser().get("/static/htmx.min.js"); res.StatusCode != http.StatusOK || len(res.body) < 10000 {
		t.Errorf("htmx not served: %d", res.StatusCode)
	}
}

// An external client (say a future mobile app) using only the OAuth endpoints.
func TestExternalClientCodeFlow(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	form := url.Values{
		"response_type": {"code"}, "client_id": {"web"}, "redirect_uri": {e.base + "/auth/callback"},
		"state": {"s"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"}, "email": {"parent@example.com"}, "password": {"correct horse"},
	}
	res := b.do(b.single, "POST", "/oauth/authorize", form)
	loc, _ := url.Parse(res.Header.Get("Location"))

	res = b.post("/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"web"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {e.base + "/auth/callback"}, "code_verifier": {verifier},
	})
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal([]byte(res.body), &tokens)
	if res.StatusCode != http.StatusOK || tokens.AccessToken == "" {
		t.Fatalf("token: %d %s", res.StatusCode, res.body)
	}
	if res = b.post("/oauth/revoke", url.Values{"token": {tokens.RefreshToken}}); res.StatusCode != http.StatusOK {
		t.Errorf("revoke: %d", res.StatusCode)
	}
}
