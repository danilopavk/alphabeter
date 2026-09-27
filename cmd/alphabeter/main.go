// Command alphabeter runs the web app. It is configured by environment variables:
//
//	DATABASE_URL   Postgres connection URL
//	REDIS_URL      Redis/Valkey connection URL (Redis 7+: uses EXPIRE NX)
//	BASE_URL       public URL, e.g. https://alphabeter.example (default http://localhost:8090)
//	ADDR           listen address; otherwise :$PORT; otherwise :8090
//	COOKIE_SECURE  "false" to allow cookies over plain http (local development only)
//	TRUST_PROXY    "true"/"false": take the visitor's IP from X-Forwarded-For.
//	               Defaults to true on Railway (RAILWAY_PROJECT_ID set), false elsewhere.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/danilopavk/alphabeter/internal/app"
	"github.com/danilopavk/alphabeter/internal/db"
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

	addr := listenAddr(os.Getenv)
	baseURL := strings.TrimSuffix(env("BASE_URL", "http://localhost:8090"), "/")

	pool, err := db.Open(ctx, env("DATABASE_URL", "postgres://alphabeter:alphabeter@localhost:5432/alphabeter?sslmode=disable"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	redisOpts, err := redis.ParseURL(env("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		return err
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	a := app.New(app.Config{
		BaseURL:       baseURL,
		SecureCookies: env("COOKIE_SECURE", "true") != "false",
		TrustProxy:    trustProxy(os.Getenv),
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
		Addr:              addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "base_url", baseURL, "trust_proxy", trustProxy(os.Getenv))
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

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
