package idp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/danilopavk/alphabeter/internal/ui"
)

type server struct {
	*fixture
	mux *http.ServeMux
}

func setupServer(t *testing.T) *server {
	f := setup(t)
	mux := http.NewServeMux()
	NewHandler(f.svc, ui.NewRenderer()).Routes(mux)
	return &server{fixture: f, mux: mux}
}

func (s *server) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func authParams() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {"web"},
		"redirect_uri":          {testRedirect},
		"state":                 {"st4te"},
		"code_challenge":        {challengeFor(testVerifier)},
		"code_challenge_method": {"S256"},
	}
}

func with(v url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == "" {
			out.Del(kv[i])
		} else {
			out.Set(kv[i], kv[i+1])
		}
	}
	return out
}

func redirectQuery(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302; body: %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != testRedirect {
		t.Fatalf("redirected to %s, want %s", got, testRedirect)
	}
	return loc.Query()
}

func TestAuthorizeShowsLoginForm(t *testing.T) {
	s := setupServer(t)
	rec := s.do("GET", "/oauth/authorize?"+authParams().Encode(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/oauth/authorize"`, `method="post"`,
		`name="email"`, `name="password"`,
		`name="state" value="st4te"`, `name="client_id" value="web"`,
		`href="/oauth/register?`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page lacks %s", want)
		}
	}
}

func TestAuthorizeRejectsBadClientWithoutRedirecting(t *testing.T) {
	s := setupServer(t)
	for name, params := range map[string]url.Values{
		"unknown client": with(authParams(), "client_id", "evil"),
		"bad redirect":   with(authParams(), "redirect_uri", "http://evil.test/cb"),
	} {
		rec := s.do("GET", "/oauth/authorize?"+params.Encode(), nil)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s: status %d, location %q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestAuthorizeRedirectsProtocolErrors(t *testing.T) {
	s := setupServer(t)
	cases := map[string]struct {
		params url.Values
		want   string
	}{
		"implicit flow":     {with(authParams(), "response_type", "token"), "unsupported_response_type"},
		"no PKCE":           {with(authParams(), "code_challenge", ""), "invalid_request"},
		"plain PKCE method": {with(authParams(), "code_challenge_method", "plain"), "invalid_request"},
	}
	for name, c := range cases {
		q := redirectQuery(t, s.do("GET", "/oauth/authorize?"+c.params.Encode(), nil))
		if q.Get("error") != c.want || q.Get("state") != "st4te" {
			t.Errorf("%s: redirect query %v, want error=%s", name, q, c.want)
		}
	}
}

func TestAuthorizeLogsInAndIssuesCode(t *testing.T) {
	s := setupServer(t)
	s.register(t, "parent@example.com")

	q := redirectQuery(t, s.do("POST", "/oauth/authorize",
		with(authParams(), "email", "parent@example.com", "password", "correct horse")))
	if q.Get("state") != "st4te" || q.Get("code") == "" {
		t.Fatalf("redirect query %v", q)
	}
	if _, err := s.svc.ExchangeCode(t.Context(), "web", q.Get("code"), testRedirect, testVerifier); err != nil {
		t.Errorf("issued code does not exchange: %v", err)
	}
}

func TestAuthorizeWrongPasswordShowsForm(t *testing.T) {
	s := setupServer(t)
	s.register(t, "parent@example.com")
	rec := s.do("POST", "/oauth/authorize", with(authParams(), "email", "parent@example.com", "password", "nope nope"))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Invalid email or password") || !strings.Contains(body, `value="parent@example.com"`) {
		t.Errorf("form does not explain the error or lost the email: %s", body)
	}
}

func TestAuthorizeRateLimit(t *testing.T) {
	s := setupServer(t)
	s.svc.cfg.LoginLimit = 1
	s.do("POST", "/oauth/authorize", with(authParams(), "email", "a@example.com", "password", "wrong wrong"))
	rec := s.do("POST", "/oauth/authorize", with(authParams(), "email", "a@example.com", "password", "wrong wrong"))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "Too many attempts") {
		t.Errorf("status %d", rec.Code)
	}
}

func TestRegisterCreatesAccountAndIssuesCode(t *testing.T) {
	s := setupServer(t)
	rec := s.do("GET", "/oauth/register?"+authParams().Encode(), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/oauth/register"`) {
		t.Fatalf("register page: %d", rec.Code)
	}

	q := redirectQuery(t, s.do("POST", "/oauth/register",
		with(authParams(), "email", "new@example.com", "password", "long enough")))
	if q.Get("code") == "" || q.Get("state") != "st4te" {
		t.Fatalf("redirect query %v", q)
	}

	for name, c := range map[string]struct {
		email, password, message string
	}{
		"duplicate":     {"new@example.com", "long enough", "already registered"},
		"weak password": {"other@example.com", "short", "at least 8 characters"},
		"bad email":     {"nope", "long enough", "valid email"},
	} {
		rec := s.do("POST", "/oauth/register", with(authParams(), "email", c.email, "password", c.password))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.message) {
			t.Errorf("%s: status %d, body lacks %q", name, rec.Code, c.message)
		}
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

func decodeToken(t *testing.T, rec *httptest.ResponseRecorder) tokenResponse {
	t.Helper()
	var tr tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body, err)
	}
	return tr
}

func TestTokenEndpoint(t *testing.T) {
	s := setupServer(t)
	id := s.register(t, "parent@example.com")
	code, _ := s.svc.IssueCode(t.Context(), id, "web", testRedirect, challengeFor(testVerifier))

	rec := s.do("POST", "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"web"}, "code": {code},
		"redirect_uri": {testRedirect}, "code_verifier": {testVerifier},
	})
	tr := decodeToken(t, rec)
	if rec.Code != http.StatusOK || tr.TokenType != "Bearer" || tr.ExpiresIn != 900 || tr.AccessToken == "" || tr.RefreshToken == "" {
		t.Fatalf("status %d, %+v", rec.Code, tr)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("token response must not be cached")
	}

	rec = s.do("POST", "/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "client_id": {"web"}, "refresh_token": {tr.RefreshToken},
	})
	if next := decodeToken(t, rec); rec.Code != http.StatusOK || next.RefreshToken == tr.RefreshToken {
		t.Errorf("refresh grant: status %d, %+v", rec.Code, next)
	}

	errorsCases := map[string]struct {
		form   url.Values
		status int
		err    string
	}{
		"reused code": {url.Values{"grant_type": {"authorization_code"}, "client_id": {"web"}, "code": {code},
			"redirect_uri": {testRedirect}, "code_verifier": {testVerifier}}, 400, "invalid_grant"},
		"unknown client":    {url.Values{"grant_type": {"refresh_token"}, "client_id": {"evil"}}, 401, "invalid_client"},
		"unsupported grant": {url.Values{"grant_type": {"password"}, "client_id": {"web"}}, 400, "unsupported_grant_type"},
	}
	for name, c := range errorsCases {
		rec := s.do("POST", "/oauth/token", c.form)
		if tr := decodeToken(t, rec); rec.Code != c.status || tr.Error != c.err {
			t.Errorf("%s: status %d error %q, want %d %q", name, rec.Code, tr.Error, c.status, c.err)
		}
	}
}

func TestRevokeEndpoint(t *testing.T) {
	s := setupServer(t)
	tokens := s.login(t, s.register(t, "parent@example.com"))
	rec := s.do("POST", "/oauth/revoke", url.Values{"token": {tokens.RefreshToken}, "client_id": {"web"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := s.svc.Refresh(t.Context(), "web", tokens.RefreshToken); err == nil {
		t.Error("refresh token still works after revocation")
	}
}
