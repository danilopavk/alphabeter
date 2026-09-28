package main

import (
	"net/mail"
	"strings"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestListenAddr(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, ":8090"},
		{map[string]string{"PORT": "3000"}, ":3000"},
		{map[string]string{"ADDR": "127.0.0.1:9000", "PORT": "3000"}, "127.0.0.1:9000"},
	}
	for _, c := range cases {
		if got := listenAddr(envOf(c.env)); got != c.want {
			t.Errorf("listenAddr(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestTrustProxy(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"RAILWAY_PROJECT_ID": "abc"}, true},
		{map[string]string{"RAILWAY_PROJECT_ID": "abc", "TRUST_PROXY": "false"}, false},
		{map[string]string{"TRUST_PROXY": "true"}, true},
	}
	for _, c := range cases {
		if got := trustProxy(envOf(c.env)); got != c.want {
			t.Errorf("trustProxy(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	full := map[string]string{
		"DATABASE_URL": "postgres://u:p@postgres.railway.internal:5432/railway\n\n",
		"REDIS_URL":    "  redis://default:p@redis.railway.internal:6379 ",
		"BASE_URL":     "https://alphabeter.example/",
		"PORT":         "8080",
	}
	cfg, err := loadConfig(envOf(full))
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		DatabaseURL:   "postgres://u:p@postgres.railway.internal:5432/railway",
		RedisURL:      "redis://default:p@redis.railway.internal:6379",
		BaseURL:       "https://alphabeter.example",
		Addr:          ":8080",
		SecureCookies: true,
	}
	if cfg != want {
		t.Errorf("config = %+v\nwant %+v", cfg, want)
	}

	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, "missing environment variables: DATABASE_URL, REDIS_URL, BASE_URL"},
		{"an unresolved Railway reference is empty", map[string]string{
			"DATABASE_URL": "", "REDIS_URL": "redis://x", "BASE_URL": "https://a.example"},
			"missing environment variables: DATABASE_URL"},
		{"whitespace only", map[string]string{
			"DATABASE_URL": "postgres://x", "REDIS_URL": " \n", "BASE_URL": "https://a.example"},
			"missing environment variables: REDIS_URL"},
		{"BASE_URL without a scheme", map[string]string{
			"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "BASE_URL": "alphabeter.example"},
			`BASE_URL must be an absolute http(s) URL, got "alphabeter.example"`},
	}
	for _, c := range cases {
		if _, err := loadConfig(envOf(c.env)); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestLoadConfigCookieAndProxySettings(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "BASE_URL": "http://localhost:8090",
		"COOKIE_SECURE": "false", "RAILWAY_PROJECT_ID": "abc", "RESEND_API_KEY": "re_x",
	}
	cfg, err := loadConfig(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecureCookies || !cfg.TrustProxy {
		t.Errorf("secure cookies %v, trust proxy %v", cfg.SecureCookies, cfg.TrustProxy)
	}
}

func TestLoadConfigEmail(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "BASE_URL": "http://localhost:8090"}
	with := func(kv ...string) map[string]string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}

	cfg, err := loadConfig(envOf(base))
	if err != nil || cfg.ResendAPIKey != "" {
		t.Errorf("locally without a key: %+v, %v", cfg, err)
	}
	cfg, err = loadConfig(envOf(with("RESEND_API_KEY", " re_123 \n")))
	if err != nil || cfg.ResendAPIKey != "re_123" {
		t.Errorf("with a key: %+v, %v", cfg, err)
	}
	_, err = loadConfig(envOf(with("RAILWAY_PROJECT_ID", "abc")))
	if err == nil || err.Error() != "missing environment variables: RESEND_API_KEY" {
		t.Errorf("on Railway without a key: err = %v", err)
	}
}

func TestMailFromIsOnTheResendDomain(t *testing.T) {
	addr, err := mail.ParseAddress(mailFrom)
	if err != nil || !strings.HasSuffix(addr.Address, "@alphabeter.org") {
		t.Errorf("mailFrom %q: %v", mailFrom, err)
	}
}
