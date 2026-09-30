package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
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
	"github.com/danilopavk/alphabeter/internal/email"
	"github.com/danilopavk/alphabeter/internal/game"
	"github.com/danilopavk/alphabeter/internal/testutil"
	"github.com/danilopavk/alphabeter/internal/ui"
)

type env struct {
	t    *testing.T
	base string
	app  *app.App
	pool *pgxpool.Pool
	mail *email.Recorder
}

func start(t *testing.T, options ...func(*app.Config)) *env {
	pool := testutil.Postgres(t)
	rdb, prefix := testutil.Redis(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	mail := &email.Recorder{}
	cfg := app.Config{BaseURL: base, KeyPrefix: prefix, Mailer: mail, MailFrom: "Alphabeter <no-reply@alphabeter.test>"}
	for _, option := range options {
		option(&cfg)
	}
	a := app.New(cfg, pool, rdb)
	srv := httptest.NewUnstartedServer(a.Handler)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &env{t: t, base: base, app: a, pool: pool, mail: mail}
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
// signUp registers, opens the emailed verification link and logs in, landing on /kids.
func (b *browser) signUp(address string) {
	b.env.t.Helper()
	b.register(address)
	b.openLink(address, "/oauth/verify")
	b.logIn(address, "correct horse")
}

// loginForm follows the redirects from /kids to the login page and returns its OAuth parameters.
func (b *browser) loginForm() url.Values {
	t := b.env.t
	t.Helper()
	page := b.get("/kids")
	if page.Request.URL.Path != "/oauth/authorize" {
		t.Fatalf("unauthenticated /kids ended at %s", page.Request.URL)
	}
	return page.Request.URL.Query()
}

func (b *browser) register(address string) response {
	t := b.env.t
	t.Helper()
	form := b.loginForm()
	form.Set("email", address)
	form.Set("password", "correct horse")
	res := b.post("/oauth/register", form)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.body, "Check your email") {
		t.Fatalf("sign-up: %d %s", res.StatusCode, res.body)
	}
	return res
}

var tokenLink = regexp.MustCompile(`https?://[^\s"]+\?token=[A-Za-z0-9_-]+`)

// openLink opens the link in the last email sent to the address; path is what the link must lead to.
func (b *browser) openLink(address, path string) response {
	t := b.env.t
	t.Helper()
	m, ok := b.env.mail.Last(address)
	if !ok {
		t.Fatalf("no email for %s", address)
	}
	link := tokenLink.FindString(m.Text)
	if !strings.HasPrefix(link, b.env.base+path+"?token=") {
		t.Fatalf("email link %q does not lead to %s", link, path)
	}
	return b.get(strings.TrimPrefix(link, b.env.base))
}

func (b *browser) logIn(address, password string) response {
	t := b.env.t
	t.Helper()
	form := b.loginForm()
	form.Set("email", address)
	form.Set("password", password)
	kids := b.post("/oauth/authorize", form)
	if kids.StatusCode != http.StatusOK || kids.Request.URL.Path != "/kids" {
		t.Fatalf("login ended at %s with %d: %s", kids.Request.URL, kids.StatusCode, kids.body)
	}
	return kids
}

var kidSlugPattern = regexp.MustCompile(`/kids/([^/"]+)/play`)

// addKid adds a kid and returns their URL segment.
func (b *browser) addKid(name string) string {
	t := b.env.t
	t.Helper()
	res := b.post("/kids", url.Values{"name": {name}}, "HX-Request", "true")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.body, name) {
		t.Fatalf("add kid: %d %s", res.StatusCode, res.body)
	}
	slugs := kidSlugPattern.FindAllStringSubmatch(res.body, -1)
	return slugs[len(slugs)-1][1]
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

func TestLandingPage(t *testing.T) {
	e := start(t)
	b := e.browser()
	res := b.do(b.single, "GET", "/", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/ logged out: %d → %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, want := range []string{
		"How it works", `href="/signup"`, `href="/login"`, "A, I, K, M, N, P, 0, 1 and 2",
		`<li class="tile retired">`, tileHTML("locked", "B", ""),
	} {
		if !strings.Contains(res.body, want) {
			t.Errorf("landing page lacks %s", want)
		}
	}

	signup := b.get("/signup")
	if signup.Request.URL.Path != "/oauth/register" || !strings.Contains(signup.body, "<h1>Create an account</h1>") ||
		signup.Request.URL.Query().Get("code_challenge") == "" {
		t.Errorf("/signup ended at %s: %s", signup.Request.URL, signup.body)
	}
	for _, path := range []string{"/signup", "/login"} {
		if page := b.get(path); !strings.Contains(page.body, `<a href="/">Alphabeter</a>`) {
			t.Errorf("%s (at %s) has no link to the home page", path, page.Request.URL.Path)
		}
	}

	b.signUp("parent@example.com")
	if res := b.get("/"); res.Request.URL.Path != "/kids" {
		t.Errorf("/ logged in ended at %s", res.Request.URL)
	}
}

func TestSEO(t *testing.T) {
	e := start(t)
	b := e.browser()
	pages := map[string][]string{
		"/": {
			"<title>Learn letters and numbers with your toddler · Alphabeter</title>",
			`<meta name="description" content="A letters-and-numbers game for toddlers, played with a parent.`,
			`<link rel="canonical" href="` + e.base + `/">`,
			`<meta property="og:title" content="Learn letters and numbers with your toddler">`,
			`<meta property="og:url" content="` + e.base + `/">`,
		},
		"/privacy": {
			`<meta name="description" content="What Alphabeter stores about you and your kids`,
			`<link rel="canonical" href="` + e.base + `/privacy">`,
		},
	}
	for path, wants := range pages {
		res := b.get(path)
		for _, want := range wants {
			if !strings.Contains(res.body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
		if strings.Contains(res.body, "noindex") {
			t.Errorf("%s is kept out of search results", path)
		}
	}
	noindex := `<meta name="robots" content="noindex">`
	for _, path := range []string{"/login", "/signup"} {
		if res := b.get(path); !strings.Contains(res.body, noindex) {
			t.Errorf("%s (at %s) can be indexed", path, res.Request.URL.Path)
		}
	}
	b.signUp("parent@example.com")
	if res := b.get("/kids"); !strings.Contains(res.body, noindex) {
		t.Error("/kids can be indexed")
	}

	robots := b.get("/robots.txt")
	if robots.StatusCode != http.StatusOK || !strings.HasPrefix(robots.Header.Get("Content-Type"), "text/plain") ||
		!strings.Contains(robots.body, "Sitemap: "+e.base+"/sitemap.xml") || strings.Contains(robots.body, "Disallow: /") {
		t.Errorf("robots.txt: %d %s\n%s", robots.StatusCode, robots.Header.Get("Content-Type"), robots.body)
	}

	sitemap := b.get("/sitemap.xml")
	var urls struct {
		Locs []string `xml:"url>loc"`
	}
	if err := xml.Unmarshal([]byte(sitemap.body), &urls); err != nil || sitemap.Header.Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatalf("sitemap.xml: %v, %s\n%s", err, sitemap.Header.Get("Content-Type"), sitemap.body)
	}
	if want := []string{e.base + "/", e.base + "/privacy"}; !slices.Equal(urls.Locs, want) {
		t.Errorf("sitemap lists %v, want %v", urls.Locs, want)
	}
}

func TestLoginSetsSessionCookies(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.register("parent@example.com")
	b.openLink("parent@example.com", "/oauth/verify")
	form := b.loginForm()
	form.Set("email", "parent@example.com")
	form.Set("password", "correct horse")

	res := b.do(b.single, "POST", "/oauth/authorize", form) // → 302 to /auth/callback
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

func TestSignUpRequiresVerification(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.register("parent@example.com")

	form := b.loginForm()
	form.Set("email", "parent@example.com")
	form.Set("password", "correct horse")
	res := b.post("/oauth/authorize", form)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(res.body, "Verify your email first") {
		t.Fatalf("login before verifying: %d %s", res.StatusCode, res.body)
	}
	if b.cookie("at") != "" {
		t.Error("got a session before verifying")
	}

	first, _ := e.mail.Last("parent@example.com")
	res = b.post("/oauth/verify/resend", url.Values{"email": {"parent@example.com"}})
	if !strings.Contains(res.body, "Check your email") {
		t.Fatalf("resend: %d %s", res.StatusCode, res.body)
	}
	if again, _ := e.mail.Last("parent@example.com"); again.Text == first.Text {
		t.Error("resend did not send a new link")
	}
	if res := b.openLink("parent@example.com", "/oauth/verify"); !strings.Contains(res.body, "Email verified") {
		t.Fatalf("verify: %d %s", res.StatusCode, res.body)
	}
	b.logIn("parent@example.com", "correct horse")
}

func TestPasswordReset(t *testing.T) {
	e := start(t)
	phone := e.browser()
	phone.signUp("parent@example.com")

	laptop := e.browser()
	res := laptop.post("/oauth/reset", url.Values{"email": {"parent@example.com"}})
	if !strings.Contains(res.body, "Check your email") {
		t.Fatalf("request reset: %d %s", res.StatusCode, res.body)
	}
	res = laptop.openLink("parent@example.com", "/oauth/reset/confirm")
	token := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(res.body)
	if token == nil {
		t.Fatalf("no reset form: %s", res.body)
	}
	res = laptop.post("/oauth/reset/confirm", url.Values{"token": {token[1]}, "password": {"a new password"}})
	if !strings.Contains(res.body, "Password changed") {
		t.Fatalf("reset: %d %s", res.StatusCode, res.body)
	}
	laptop.logIn("parent@example.com", "a new password")

	// The phone's session ends once its access token expires.
	e.app.IDP.Revoke(context.Background(), phone.cookie("at"))
	if res := phone.do(phone.single, "GET", "/kids", nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("phone still logged in after the reset: %d", res.StatusCode)
	}
}

func TestUnverifiedAccountsAreLoggedOut(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	e.pool.Exec(context.Background(), `UPDATE users SET email_verified_at = NULL`)
	e.app.IDP.Revoke(context.Background(), b.cookie("at")) // as if the access token expired
	if res := b.do(b.single, "GET", "/kids", nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("unverified account still logged in: %d", res.StatusCode)
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
	slug := b.addKid("Mia")

	page := b.get("/kids/" + slug + "/play")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.body, `name="seq" value="0"`) {
		t.Fatalf("play page: %d %s", page.StatusCode, page.body)
	}
	symbol := symbolPattern.FindStringSubmatch(page.body)
	if symbol == nil {
		t.Fatal("no symbol on the play page")
	}
	if s, ok := game.ParseSymbol(symbol[1]); !ok || !slices.Contains(game.Batches[0], s) {
		t.Errorf("first symbol %q is not from batch 1", symbol[1])
	}
	for _, want := range []string{
		`<span class="score-value">` + symbol[1] + ` score: 100</span>`,
		`<span class="score-note">Over 130 needed for the next level</span>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("play page lacks %s", want)
		}
	}
	if strings.Contains(page.body, `class="flash`) {
		t.Error("a fresh play page shows an answer flash")
	}

	answer := func(seq string) response {
		return b.post("/kids/"+slug+"/answer", url.Values{"seq": {seq}, "correct": {"1"}}, "HX-Request", "true")
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
	e.pool.QueryRow(context.Background(), `SELECT seq FROM kids WHERE slug = $1`, slug).Scan(&seq)
	if seq != 1 {
		t.Errorf("seq in DB = %d, want 1", seq)
	}

	// Without htmx the form still works.
	res = b.post("/kids/"+slug+"/answer", url.Values{"seq": {"1"}, "correct": {"0"}})
	if res.Request.URL.Path != "/kids/mia/play" || !strings.Contains(res.body, `name="seq" value="2"`) {
		t.Errorf("plain form post ended at %s", res.Request.URL)
	}
}

// tileHTML is how the progress board shows one symbol.
func tileHTML(status, symbol, score string) string {
	html := `<li class="tile ` + status + `"><span class="tile-symbol">` + symbol + `</span>`
	if score != "" {
		html += `<span class="tile-score">` + score + `</span>`
	}
	return html + `</li>`
}

var symbolPattern = regexp.MustCompile(`<div class="symbol"[^>]*>(.)</div>`)

func TestDashboard(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	slug := b.addKid("Mia")

	list := b.get("/kids")
	for _, want := range []string{
		tileHTML("learning", "A", "100"),
		tileHTML("learning", "2", "100"),
		tileHTML("locked", "B", ""),
		`<span class="swatch known"></span>Over 130`,
	} {
		if !strings.Contains(list.body, want) {
			t.Errorf("kids page lacks %s", want)
		}
	}

	page := b.get("/kids/" + slug + "/play")
	for _, want := range []string{
		`<script src="` + ui.Asset("play.js") + `" defer></script>`,
		`<button type="button" class="score" data-board-toggle`,
		`<span class="score-hint">Click on a score or press d to open the scorecard</span>`,
		`<section id="board" class="board-panel"`,
		tileHTML("locked", "Z", ""),
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("play page lacks %s", want)
		}
	}

	// The board is part of the swapped stage, so it stays current while open.
	first := symbolPattern.FindStringSubmatch(page.body)[1]
	res := b.post("/kids/"+slug+"/answer", url.Values{"seq": {"0"}, "correct": {"1"}}, "HX-Request", "true")
	if want := tileHTML("learning", first, "100.5"); !strings.Contains(res.body, want) {
		t.Errorf("answer fragment lacks %s", want)
	}
	second := symbolPattern.FindStringSubmatch(res.body)[1]
	b.post("/kids/"+slug+"/answer", url.Values{"seq": {"1"}, "correct": {"0"}}, "HX-Request", "true")
	if want := tileHTML("low", second, "99.5"); !strings.Contains(b.get("/kids").body, want) {
		t.Errorf("kids page lacks %s after a wrong answer", want)
	}
}

func TestKidURLsUseTheNameNotTheID(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	if slug := b.addKid("Ana Marija"); slug != "ana-marija" {
		t.Fatalf("Ana Marija's URL segment is %q", slug)
	}
	page := b.get("/kids/ana-marija/play")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.body, `hx-post="/kids/ana-marija/answer"`) {
		t.Errorf("play page: %d", page.StatusCode)
	}
	var id string
	e.pool.QueryRow(context.Background(), `SELECT id::text FROM kids WHERE slug = 'ana-marija'`).Scan(&id)
	for _, old := range []string{"/kids/" + id + "/play", "/kids/" + id + "-ana-marija/play"} {
		if res := b.get(old); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", old, res.StatusCode)
		}
	}

	res := b.post("/kids", url.Values{"name": {"Ana-Marija"}}, "HX-Request", "true")
	if !strings.Contains(res.body, "You already have a kid with that name.") {
		t.Errorf("second Ana-Marija: %d %s", res.StatusCode, res.body)
	}

	// Letters outside ASCII survive the round trip through the link.
	b.addKid("Đorđe")
	link := regexp.MustCompile(`href="(/kids/[^/"]+/play)"`).FindAllStringSubmatch(b.get("/kids").body, -1)
	if res := b.get(link[len(link)-1][1]); res.StatusCode != http.StatusOK || !strings.Contains(res.body, "<title>Đorđe") {
		t.Errorf("GET %s: %d", link[len(link)-1][1], res.StatusCode)
	}
}

func TestKidsBelongToTheirParent(t *testing.T) {
	e := start(t)
	owner := e.browser()
	owner.signUp("parent@example.com")
	slug := owner.addKid("Mia")

	other := e.browser()
	other.signUp("other@example.com")
	if res := other.get("/kids/" + slug + "/play"); res.StatusCode != http.StatusNotFound {
		t.Errorf("play: %d", res.StatusCode)
	}
	if res := other.post("/kids/"+slug+"/answer", url.Values{"seq": {"0"}, "correct": {"1"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("answer: %d", res.StatusCode)
	}
	if res := other.post("/kids/"+slug+"/delete", nil, "HX-Request", "true"); res.StatusCode != http.StatusNotFound {
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
	slug := b.addKid("Mia")
	res = b.post("/kids/"+slug+"/delete", nil, "HX-Request", "true")
	if res.StatusCode != http.StatusOK || strings.Contains(res.body, "Mia") {
		t.Errorf("after delete: %d %s", res.StatusCode, res.body)
	}
}

func TestDeleteAccount(t *testing.T) {
	e := start(t)
	b := e.browser()
	b.signUp("parent@example.com")
	b.addKid("Mia")
	if list := b.get("/kids"); !strings.Contains(list.body, `href="/account/delete"`) || !strings.Contains(list.body, `href="/privacy"`) {
		t.Errorf("kids page lacks the delete account or privacy link")
	}
	if page := b.get("/account/delete"); page.StatusCode != http.StatusOK || !strings.Contains(page.body, `name="password"`) {
		t.Fatalf("delete page: %d %s", page.StatusCode, page.body)
	}

	res := b.post("/account/delete", url.Values{"password": {"wrong password"}})
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.body, "That password isn&#39;t right.") {
		t.Errorf("wrong password: %d %s", res.StatusCode, res.body)
	}
	res = b.post("/account/delete", url.Values{"password": {"correct horse"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(res.body, "Account deleted") {
		t.Fatalf("delete: %d %s", res.StatusCode, res.body)
	}
	if b.cookie("at") != "" || b.cookie("rt") != "" {
		t.Error("session cookies survive the deletion")
	}
	var users, kids int
	e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM kids)`).Scan(&users, &kids)
	if users != 0 || kids != 0 {
		t.Errorf("%d users and %d kids left", users, kids)
	}

	// The address is free again, and the new account starts empty.
	b.signUp("parent@example.com")
	if list := b.get("/kids"); strings.Contains(list.body, "Mia") {
		t.Error("the new account has the old account's kid")
	}
}

func TestPrivacyPage(t *testing.T) {
	e := start(t)
	b := e.browser()
	res := b.get("/privacy")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/privacy: %d", res.StatusCode)
	}
	for _, want := range []string{
		"United States", "kept for 30 days", "<td>rt</td>", "<td>30 days after your last visit</td>",
		"<td>15 minutes, renewed automatically</td>", "<td>10 minutes</td>",
		`<a href="mailto:info@alphabeter.org">info@alphabeter.org</a>`,
	} {
		if !strings.Contains(res.body, want) {
			t.Errorf("privacy page lacks %s", want)
		}
	}
	for _, path := range []string{"/", "/signup"} {
		if page := b.get(path); !strings.Contains(page.body, `href="/privacy"`) {
			t.Errorf("%s (at %s) does not link to the privacy page", path, page.Request.URL.Path)
		}
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
		return b.do(b.single, "POST", "/oauth/authorize", form, "CF-Connecting-IP", ip).StatusCode
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
