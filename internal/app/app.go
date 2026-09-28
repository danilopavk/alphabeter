// Package app wires the identity provider, the web app and static assets into
// one HTTP handler.
package app

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danilopavk/alphabeter/internal/clientip"
	"github.com/danilopavk/alphabeter/internal/email"
	"github.com/danilopavk/alphabeter/internal/idp"
	"github.com/danilopavk/alphabeter/internal/kids"
	"github.com/danilopavk/alphabeter/internal/ui"
	"github.com/danilopavk/alphabeter/internal/web"
)

type Config struct {
	BaseURL       string // public URL of the app, without trailing slash
	SecureCookies bool
	KeyPrefix     string // Redis key prefix
	TrustProxy    bool   // traffic arrives only through Railway's edge; see clientip.Resolver
	Mailer        email.Sender
	MailFrom      string // "Alphabeter <no-reply@alphabeter.example>"
}

type App struct {
	Handler http.Handler
	IDP     *idp.Service
}

func New(cfg Config, pool *pgxpool.Pool, rdb *redis.Client) *App {
	idpSvc := idp.NewService(pool, rdb, idp.Config{
		KeyPrefix: cfg.KeyPrefix,
		Clients:   []idp.Client{{ID: web.ClientID, RedirectURIs: []string{cfg.BaseURL + web.CallbackPath}}},
		PublicURL: cfg.BaseURL,
		MailFrom:  cfg.MailFrom,
		Mailer:    cfg.Mailer,
	})
	renderer := ui.NewRenderer()

	mux := http.NewServeMux()
	idp.NewHandler(idpSvc, renderer).Routes(mux)
	web.New(web.Config{BaseURL: cfg.BaseURL, SecureCookies: cfg.SecureCookies}, idpSvc, kids.NewService(pool), renderer).Routes(mux)
	mux.Handle("GET /static/", ui.Static())

	handler := clientip.Resolver{TrustProxy: cfg.TrustProxy}.Middleware(mux)
	return &App{Handler: web.Secure(cfg.BaseURL, handler), IDP: idpSvc}
}
