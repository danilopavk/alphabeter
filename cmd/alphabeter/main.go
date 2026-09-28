// Command alphabeter runs the web app. It is configured by environment variables:
//
//	DATABASE_URL   required: Postgres connection URL
//	REDIS_URL      required: Redis/Valkey connection URL (Redis 7+: uses EXPIRE NX)
//	BASE_URL       required: public URL, e.g. https://alphabeter.example
//	ADDR           listen address; otherwise :$PORT; otherwise :8090
//	COOKIE_SECURE  "false" to allow cookies over plain http (local development only)
//	TRUST_PROXY    "true"/"false": take the visitor's IP from X-Forwarded-For.
//	               Defaults to true on Railway (RAILWAY_PROJECT_ID set), false elsewhere.
//	RESEND_API_KEY for sending emails; required on Railway. Without it, emails are
//	               printed to the log instead (local development).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/danilopavk/alphabeter/internal/app"
	"github.com/danilopavk/alphabeter/internal/db"
	"github.com/danilopavk/alphabeter/internal/email"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	var mailer email.Sender = email.Log{}
	if cfg.ResendAPIKey != "" {
		mailer = &email.Resend{APIKey: cfg.ResendAPIKey}
	} else {
		slog.Warn("RESEND_API_KEY not set: emails are printed to the log, not sent")
	}

	a := app.New(app.Config{
		BaseURL:       cfg.BaseURL,
		SecureCookies: cfg.SecureCookies,
		TrustProxy:    cfg.TrustProxy,
		Mailer:        mailer,
		MailFrom:      mailFrom,
	}, pool, rdb)

	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			if n, err := a.IDP.DeleteExpiredRefreshTokens(ctx); err != nil {
				slog.Error("delete expired refresh tokens", "err", err)
			} else if n > 0 {
				slog.Info("deleted expired refresh tokens", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL, "trust_proxy", cfg.TrustProxy)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type config struct {
	DatabaseURL   string
	RedisURL      string
	BaseURL       string // without trailing slash
	Addr          string
	SecureCookies bool
	TrustProxy    bool
	ResendAPIKey  string
}

// mailFrom sends every email, locally too; it must be on the domain verified in Resend.
const mailFrom = "Alphabeter <no-reply@alphabeter.org>"

// loadConfig reads the environment. Missing required variables are all named
// in one error; on Railway an unresolved variable reference arrives empty.
func loadConfig(getenv func(string) string) (config, error) {
	get := func(key string) string { return strings.TrimSpace(getenv(key)) }
	cfg := config{
		DatabaseURL:   get("DATABASE_URL"),
		RedisURL:      get("REDIS_URL"),
		BaseURL:       strings.TrimSuffix(get("BASE_URL"), "/"),
		Addr:          listenAddr(get),
		SecureCookies: get("COOKIE_SECURE") != "false",
		TrustProxy:    trustProxy(get),
		ResendAPIKey:  get("RESEND_API_KEY"),
	}
	required := []struct{ key, value string }{
		{"DATABASE_URL", cfg.DatabaseURL}, {"REDIS_URL", cfg.RedisURL}, {"BASE_URL", cfg.BaseURL},
	}
	if get("RAILWAY_PROJECT_ID") != "" {
		required = append(required, struct{ key, value string }{"RESEND_API_KEY", cfg.ResendAPIKey})
	}
	var missing []string
	for _, v := range required {
		if v.value == "" {
			missing = append(missing, v.key)
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	if u, err := url.Parse(cfg.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, fmt.Errorf("BASE_URL must be an absolute http(s) URL, got %q", cfg.BaseURL)
	}
	return cfg, nil
}

// listenAddr is ADDR if set, else :$PORT (set by hosting platforms), else :8090.
func listenAddr(getenv func(string) string) string {
	if addr := getenv("ADDR"); addr != "" {
		return addr
	}
	if port := getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8090"
}

// trustProxy reports whether requests only arrive through a proxy that
// rebuilds X-Forwarded-For. Railway's edge does, so it is the default there.
func trustProxy(getenv func(string) string) bool {
	switch getenv("TRUST_PROXY") {
	case "true":
		return true
	case "false":
		return false
	}
	return getenv("RAILWAY_PROJECT_ID") != ""
}
