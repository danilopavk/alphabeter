package idp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/danilopavk/alphabeter/internal/ui"
)

// Handler serves the OAuth 2.1 endpoints and the login/registration pages.
type Handler struct {
	svc *Service
	ui  *ui.Renderer
}

func NewHandler(svc *Service, renderer *ui.Renderer) *Handler {
	return &Handler{svc: svc, ui: renderer}
}

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/authorize", h.showLogin)
	mux.HandleFunc("POST /oauth/authorize", h.login)
	mux.HandleFunc("GET /oauth/register", h.showRegister)
	mux.HandleFunc("POST /oauth/register", h.register)
	mux.HandleFunc("POST /oauth/token", h.token)
	mux.HandleFunc("POST /oauth/revoke", h.revoke)
}

// authRequest is the authorization request. The login and registration forms
// carry it along in hidden fields.
type authRequest struct {
	ResponseType, ClientID, RedirectURI, State, CodeChallenge, CodeChallengeMethod string
}

func parseAuthRequest(v url.Values) authRequest {
	return authRequest{
		ResponseType:        v.Get("response_type"),
		ClientID:            v.Get("client_id"),
		RedirectURI:         v.Get("redirect_uri"),
		State:               v.Get("state"),
		CodeChallenge:       v.Get("code_challenge"),
		CodeChallengeMethod: v.Get("code_challenge_method"),
	}
}

type field struct{ Name, Value string }

func (a authRequest) fields() []field {
	return []field{
		{"response_type", a.ResponseType},
		{"client_id", a.ClientID},
		{"redirect_uri", a.RedirectURI},
		{"state", a.State},
		{"code_challenge", a.CodeChallenge},
		{"code_challenge_method", a.CodeChallengeMethod},
	}
}

func (a authRequest) query() string {
	v := url.Values{}
	for _, f := range a.fields() {
		v.Set(f.Name, f.Value)
	}
	return v.Encode()
}

// validate reports whether the request may proceed. Errors about the client or
// redirect URI are shown here, never redirected, so they can't be used to send
// users to an attacker's site. Other errors go back to the client.
func (h *Handler) validate(w http.ResponseWriter, r *http.Request, a authRequest) bool {
	if err := h.svc.CheckClient(a.ClientID, a.RedirectURI); err != nil {
		h.ui.Error(w, http.StatusBadRequest, "This sign-in link is invalid. Go back to the app and try again.")
		return false
	}
	switch {
	case a.ResponseType != "code":
		h.redirectError(w, r, a, "unsupported_response_type", "only response_type=code is supported")
		return false
	case a.CodeChallengeMethod != "S256" || !ValidChallenge(a.CodeChallenge):
		h.redirectError(w, r, a, "invalid_request", "an S256 code_challenge is required")
		return false
	}
	return true
}

func (h *Handler) redirectError(w http.ResponseWriter, r *http.Request, a authRequest, code, description string) {
	http.Redirect(w, r, withQuery(a.RedirectURI, url.Values{
		"error": {code}, "error_description": {description}, "state": {a.State},
	}), http.StatusFound)
}

func withQuery(rawURL string, params url.Values) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	for k, v := range params {
		if v[0] != "" {
			q[k] = v
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

type formPage struct {
	Hidden []field
	Email  string
	Error  string
	AltURL string
}

func (h *Handler) renderLogin(w http.ResponseWriter, status int, a authRequest, email, message string) {
	h.ui.Page(w, status, "login", formPage{a.fields(), email, message, "/oauth/register?" + a.query()})
}

func (h *Handler) renderRegister(w http.ResponseWriter, status int, a authRequest, email, message string) {
	h.ui.Page(w, status, "register", formPage{a.fields(), email, message, "/oauth/authorize?" + a.query()})
}

func (h *Handler) showLogin(w http.ResponseWriter, r *http.Request) {
	a := parseAuthRequest(r.URL.Query())
	if h.validate(w, r, a) {
		h.renderLogin(w, http.StatusOK, a, "", "")
	}
}

func (h *Handler) showRegister(w http.ResponseWriter, r *http.Request) {
	a := parseAuthRequest(r.URL.Query())
	if h.validate(w, r, a) {
		h.renderRegister(w, http.StatusOK, a, "", "")
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	a := parseAuthRequest(r.PostForm)
	if !h.validate(w, r, a) {
		return
	}
	email := r.PostForm.Get("email")
	userID, err := h.svc.Authenticate(r.Context(), email, r.PostForm.Get("password"), clientIP(r))
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		h.renderLogin(w, http.StatusUnauthorized, a, email, "Invalid email or password.")
	case errors.Is(err, ErrRateLimited):
		h.renderLogin(w, http.StatusTooManyRequests, a, email, "Too many attempts. Try again in a few minutes.")
	case err != nil:
		h.serverError(w, err)
	default:
		h.issueCode(w, r, a, userID)
	}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	a := parseAuthRequest(r.PostForm)
	if !h.validate(w, r, a) {
		return
	}
	email := r.PostForm.Get("email")
	userID, err := h.svc.Register(r.Context(), email, r.PostForm.Get("password"))
	switch {
	case errors.Is(err, ErrEmailTaken):
		h.renderRegister(w, http.StatusBadRequest, a, email, "That email is already registered. Log in instead?")
	case errors.Is(err, ErrWeakPassword):
		h.renderRegister(w, http.StatusBadRequest, a, email, "Use at least 8 characters (and at most 128) for the password.")
	case errors.Is(err, ErrInvalidEmail):
		h.renderRegister(w, http.StatusBadRequest, a, email, "Enter a valid email address.")
	case err != nil:
		h.serverError(w, err)
	default:
		h.issueCode(w, r, a, userID)
	}
}

func (h *Handler) issueCode(w http.ResponseWriter, r *http.Request, a authRequest, userID int64) {
	code, err := h.svc.IssueCode(r.Context(), userID, a.ClientID, a.RedirectURI, a.CodeChallenge)
	if err != nil {
		h.serverError(w, err)
		return
	}
	http.Redirect(w, r, withQuery(a.RedirectURI, url.Values{"code": {code}, "state": {a.State}}), http.StatusFound)
}

func (h *Handler) serverError(w http.ResponseWriter, err error) {
	slog.Error("idp", "err", err)
	h.ui.Error(w, http.StatusInternalServerError, "Please try again in a moment.")
}

// token is the token endpoint (RFC 6749 §3.2).
func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	form := r.PostForm
	clientID := form.Get("client_id")
	if !h.svc.KnownClient(clientID) {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	var (
		tokens Tokens
		err    error
	)
	switch form.Get("grant_type") {
	case "authorization_code":
		tokens, err = h.svc.ExchangeCode(r.Context(), clientID, form.Get("code"), form.Get("redirect_uri"), form.Get("code_verifier"))
	case "refresh_token":
		tokens, err = h.svc.Refresh(r.Context(), clientID, form.Get("refresh_token"))
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	switch {
	case errors.Is(err, ErrInvalidGrant):
		tokenError(w, http.StatusBadRequest, "invalid_grant")
	case err != nil:
		slog.Error("token endpoint", "err", err)
		tokenError(w, http.StatusInternalServerError, "server_error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  tokens.AccessToken,
			"token_type":    "Bearer",
			"expires_in":    int(tokens.ExpiresIn.Seconds()),
			"refresh_token": tokens.RefreshToken,
		})
	}
}

// revoke is the revocation endpoint (RFC 7009): it answers 200 even for
// unknown tokens.
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if err := h.svc.Revoke(r.Context(), r.PostForm.Get("token")); err != nil {
		slog.Error("revoke", "err", err)
		tokenError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
