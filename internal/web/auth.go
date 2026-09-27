// Package web is the parent-facing app. For sign-in it is an ordinary OAuth
// client of the idp package (authorization code + PKCE), exchanging the code
// server-side and keeping the tokens in HttpOnly cookies.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danilopavk/alphabeter/internal/idp"
	"github.com/danilopavk/alphabeter/internal/kids"
	"github.com/danilopavk/alphabeter/internal/ui"
)

const (
	ClientID     = "web"
	CallbackPath = "/auth/callback"

	accessCookie  = "at"
	refreshCookie = "rt"
	txCookie      = "oauth_tx" // state + "." + PKCE verifier while a sign-in is in flight
)

type Config struct {
	BaseURL       string // e.g. https://alphabeter.example
	SecureCookies bool
}

type Server struct {
	cfg  Config
	idp  *idp.Service
	kids *kids.Service
	ui   *ui.Renderer
}

func New(cfg Config, idpSvc *idp.Service, kidsSvc *kids.Service, renderer *ui.Renderer) *Server {
	return &Server{cfg: cfg, idp: idpSvc, kids: kidsSvc, ui: renderer}
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/kids", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET "+CallbackPath, s.callback)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /kids", s.authed(s.listKids))
	mux.HandleFunc("POST /kids", s.authed(s.addKid))
	mux.HandleFunc("POST /kids/{kid}/delete", s.authed(s.deleteKid))
	mux.HandleFunc("GET /kids/{kid}/play", s.authed(s.play))
	mux.HandleFunc("POST /kids/{kid}/answer", s.authed(s.answer))
}

func (s *Server) redirectURI() string { return s.cfg.BaseURL + CallbackPath }

// login starts a sign-in: remember state and verifier, send the browser to the
// authorization endpoint.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state, verifier := randomString(16), randomString(32)
	s.setCookie(w, txCookie, state+"."+verifier, CallbackPath, 10*time.Minute)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {s.redirectURI()},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, "/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tx, cookieErr := r.Cookie(txCookie)
	s.setCookie(w, txCookie, "", CallbackPath, -1)

	if q.Get("error") != "" {
		s.ui.Error(w, http.StatusBadRequest, "Sign-in was cancelled or failed. Please try again.")
		return
	}
	var state, verifier string
	if cookieErr == nil {
		state, verifier, _ = strings.Cut(tx.Value, ".")
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(q.Get("state"))) != 1 {
		s.ui.Error(w, http.StatusBadRequest, "Your sign-in expired. Please try again.")
		return
	}
	tokens, err := s.idp.ExchangeCode(r.Context(), ClientID, q.Get("code"), s.redirectURI(), verifier)
	if errors.Is(err, idp.ErrInvalidGrant) {
		s.ui.Error(w, http.StatusBadRequest, "Your sign-in expired. Please try again.")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.setSession(w, tokens)
	http.Redirect(w, r, "/kids", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{refreshCookie, accessCookie} {
		if c, err := r.Cookie(name); err == nil {
			if err := s.idp.Revoke(r.Context(), c.Value); err != nil {
				slog.Error("logout", "err", err)
			}
		}
	}
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type userHandler func(w http.ResponseWriter, r *http.Request, userID int64)

// authed resolves the signed-in user from the access token cookie, silently
// rotating the refresh token when the access token has expired.
func (s *Server) authed(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(accessCookie); err == nil {
			userID, err := s.idp.UserForAccessToken(r.Context(), c.Value)
			if err == nil {
				next(w, r, userID)
				return
			}
			if !errors.Is(err, idp.ErrInvalidToken) {
				s.serverError(w, err)
				return
			}
		}
		if c, err := r.Cookie(refreshCookie); err == nil {
			tokens, err := s.idp.Refresh(r.Context(), ClientID, c.Value)
			if err == nil {
				s.setSession(w, tokens)
				next(w, r, tokens.UserID)
				return
			}
			if !errors.Is(err, idp.ErrInvalidGrant) {
				s.serverError(w, err)
				return
			}
		}
		s.clearSession(w)
		if isHtmx(r) {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func (s *Server) setSession(w http.ResponseWriter, t idp.Tokens) {
	s.setCookie(w, accessCookie, t.AccessToken, "/", t.ExpiresIn)
	s.setCookie(w, refreshCookie, t.RefreshToken, "/", s.idp.RefreshTTL())
}

func (s *Server) clearSession(w http.ResponseWriter) {
	s.setCookie(w, accessCookie, "", "/", -1)
	s.setCookie(w, refreshCookie, "", "/", -1)
}

// setCookie sets an HttpOnly, SameSite=Lax cookie; a negative maxAge deletes it.
func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	slog.Error("web", "err", err)
	s.ui.Error(w, http.StatusInternalServerError, "Please try again in a moment.")
}

func isHtmx(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
