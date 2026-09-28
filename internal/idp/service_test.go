package idp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danilopavk/alphabeter/internal/email"
	"github.com/danilopavk/alphabeter/internal/testutil"
)

const (
	testRedirect = "http://app.test/auth/callback"
	testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk" // RFC 7636 appendix B
)

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type fixture struct {
	svc   *Service
	clock time.Time
	mail  *email.Recorder
}

func setup(t *testing.T) *fixture {
	pool := testutil.Postgres(t)
	rdb, prefix := testutil.Redis(t)
	f := &fixture{clock: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), mail: &email.Recorder{}}
	f.svc = NewService(pool, rdb, Config{
		KeyPrefix: prefix,
		Clients:   []Client{{ID: "web", RedirectURIs: []string{testRedirect}}},
		PublicURL: "https://alphabeter.test",
		MailFrom:  "Alphabeter <no-reply@alphabeter.test>",
		Mailer:    f.mail,
	})
	f.svc.now = func() time.Time { return f.clock }
	return f
}

// register creates a verified account with the password "correct horse".
func (f *fixture) register(t *testing.T, address string) int64 {
	t.Helper()
	id, err := f.svc.Register(context.Background(), address, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.pool.Exec(context.Background(), `UPDATE users SET email_verified_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// login runs the code flow and returns the issued tokens.
func (f *fixture) login(t *testing.T, userID int64) Tokens {
	t.Helper()
	ctx := context.Background()
	code, err := f.svc.IssueCode(ctx, userID, "web", testRedirect, challengeFor(testVerifier))
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := f.svc.ExchangeCode(ctx, "web", code, testRedirect, testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func TestPasswordHashing(t *testing.T) {
	h1, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h1, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash %q is not an argon2id PHC string", h1)
	}
	h2, _ := hashPassword("correct horse")
	if h1 == h2 {
		t.Error("two hashes of the same password are equal (no salt?)")
	}
	if !verifyPassword(h1, "correct horse") {
		t.Error("right password rejected")
	}
	if verifyPassword(h1, "battery staple") {
		t.Error("wrong password accepted")
	}
	for _, bad := range []string{"", "x", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "$bcrypt$..."} {
		if verifyPassword(bad, "correct horse") {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestRegister(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, err := f.svc.Register(ctx, "  Parent@Example.com ", "correct horse")
	if err != nil || id == 0 {
		t.Fatalf("Register = %d, %v", id, err)
	}
	if _, err := f.svc.Register(ctx, "parent@example.COM", "another password"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email err = %v", err)
	}
	for _, email := range []string{"", "no-at-sign", "a@", "Name <a@b.c>", strings.Repeat("a", 250) + "@b.cd"} {
		if _, err := f.svc.Register(ctx, email, "correct horse"); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("Register(%q) err = %v, want ErrInvalidEmail", email, err)
		}
	}
	for _, pw := range []string{"short", strings.Repeat("x", 129)} {
		if _, err := f.svc.Register(ctx, "new@example.com", pw); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("password of %d chars err = %v, want ErrWeakPassword", len(pw), err)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")

	if got, err := f.svc.Authenticate(ctx, "PARENT@example.com", "correct horse", "10.0.0.1"); err != nil || got != id {
		t.Errorf("Authenticate = %d, %v; want %d", got, err, id)
	}
	if _, err := f.svc.Authenticate(ctx, "parent@example.com", "wrong password", "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password err = %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, "nobody@example.com", "correct horse", "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown email err = %v", err)
	}
}

func TestAuthenticateIsRateLimitedPerEmail(t *testing.T) {
	f := setup(t)
	f.svc.cfg.LoginLimit = 3
	ctx := context.Background()
	f.register(t, "parent@example.com")
	for i := range 3 {
		ip := "10.0.0." + string(rune('1'+i)) // different IPs, same email
		if _, err := f.svc.Authenticate(ctx, "parent@example.com", "wrong", ip); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d err = %v", i, err)
		}
	}
	if _, err := f.svc.Authenticate(ctx, "parent@example.com", "correct horse", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestAuthenticateIsRateLimitedPerIP(t *testing.T) {
	f := setup(t)
	f.svc.cfg.LoginLimit = 3
	ctx := context.Background()
	f.register(t, "parent@example.com")
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		f.svc.Authenticate(ctx, email, "wrong", "10.0.0.1")
	}
	if _, err := f.svc.Authenticate(ctx, "parent@example.com", "correct horse", "10.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestCheckClient(t *testing.T) {
	f := setup(t)
	if err := f.svc.CheckClient("web", testRedirect); err != nil {
		t.Errorf("registered client rejected: %v", err)
	}
	if err := f.svc.CheckClient("evil", testRedirect); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("unknown client err = %v", err)
	}
	if err := f.svc.CheckClient("web", testRedirect+"/../x"); !errors.Is(err, ErrInvalidRedirect) {
		t.Errorf("unregistered redirect err = %v", err)
	}
}

func TestCodeExchange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	challenge := challengeFor(testVerifier)

	issue := func() string {
		code, err := f.svc.IssueCode(ctx, id, "web", testRedirect, challenge)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}

	code := issue()
	tokens, err := f.svc.ExchangeCode(ctx, "web", code, testRedirect, testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.UserID != id || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresIn != 15*time.Minute {
		t.Errorf("tokens = %+v", tokens)
	}
	if user, err := f.svc.UserForAccessToken(ctx, tokens.AccessToken); err != nil || user != id {
		t.Errorf("UserForAccessToken = %d, %v", user, err)
	}
	if _, err := f.svc.ExchangeCode(ctx, "web", code, testRedirect, testVerifier); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("reused code err = %v", err)
	}

	bad := map[string]func(code string) error{
		"wrong verifier": func(c string) error {
			_, err := f.svc.ExchangeCode(ctx, "web", c, testRedirect, strings.Repeat("a", 43))
			return err
		},
		"wrong redirect": func(c string) error {
			_, err := f.svc.ExchangeCode(ctx, "web", c, "http://app.test/other", testVerifier)
			return err
		},
		"wrong client": func(c string) error {
			_, err := f.svc.ExchangeCode(ctx, "other", c, testRedirect, testVerifier)
			return err
		},
		"unknown code": func(string) error {
			_, err := f.svc.ExchangeCode(ctx, "web", "not-a-code", testRedirect, testVerifier)
			return err
		},
	}
	for name, try := range bad {
		c := issue()
		if err := try(c); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: err = %v, want ErrInvalidGrant", name, err)
		}
		// A failed exchange still burns the code.
		if _, err := f.svc.ExchangeCode(ctx, "web", c, testRedirect, testVerifier); name != "unknown code" && !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: code usable after failed exchange", name)
		}
	}
}

func TestIssueCodeValidatesRequest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	if _, err := f.svc.IssueCode(ctx, id, "evil", testRedirect, challengeFor(testVerifier)); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("unknown client err = %v", err)
	}
	if _, err := f.svc.IssueCode(ctx, id, "web", "http://evil.test/cb", challengeFor(testVerifier)); !errors.Is(err, ErrInvalidRedirect) {
		t.Errorf("bad redirect err = %v", err)
	}
	for _, c := range []string{"", "short", strings.Repeat("a", 42) + "="} {
		if _, err := f.svc.IssueCode(ctx, id, "web", testRedirect, c); !errors.Is(err, ErrInvalidChallenge) {
			t.Errorf("challenge %q err = %v", c, err)
		}
	}
}

func TestCodesAndAccessTokensExpire(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	code, _ := f.svc.IssueCode(ctx, id, "web", testRedirect, challengeFor(testVerifier))
	if ttl := f.svc.rdb.TTL(ctx, f.svc.key("ac:", code)).Val(); ttl <= 0 || ttl > time.Minute {
		t.Errorf("code TTL = %s", ttl)
	}
	tokens, _ := f.svc.ExchangeCode(ctx, "web", code, testRedirect, testVerifier)
	if ttl := f.svc.rdb.TTL(ctx, f.svc.key("at:", tokens.AccessToken)).Val(); ttl <= 14*time.Minute || ttl > 15*time.Minute {
		t.Errorf("access token TTL = %s", ttl)
	}
	if _, err := f.svc.UserForAccessToken(ctx, "unknown"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("unknown access token err = %v", err)
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	first := f.login(t, id)
	otherDevice := f.login(t, id)

	second, err := f.svc.Refresh(ctx, "web", first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.UserID != id || second.RefreshToken == first.RefreshToken || second.AccessToken == first.AccessToken {
		t.Errorf("refresh returned %+v", second)
	}
	if user, _ := f.svc.UserForAccessToken(ctx, second.AccessToken); user != id {
		t.Error("new access token does not resolve")
	}

	// Replaying the old refresh token revokes the whole family…
	if _, err := f.svc.Refresh(ctx, "web", first.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("reuse err = %v", err)
	}
	if _, err := f.svc.Refresh(ctx, "web", second.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("family member still valid after reuse: %v", err)
	}
	// …but not other logins.
	if _, err := f.svc.Refresh(ctx, "web", otherDevice.RefreshToken); err != nil {
		t.Errorf("other family affected: %v", err)
	}
}

func TestRefreshRejects(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")

	tokens := f.login(t, id)
	if _, err := f.svc.Refresh(ctx, "other", tokens.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("wrong client err = %v", err)
	}
	if _, err := f.svc.Refresh(ctx, "web", "garbage"); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("unknown token err = %v", err)
	}

	f.clock = f.clock.Add(30*24*time.Hour + time.Second)
	if _, err := f.svc.Refresh(ctx, "web", tokens.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("expired token err = %v", err)
	}
}

func TestRefreshExpirySlides(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tokens := f.login(t, f.register(t, "parent@example.com"))
	for range 3 { // 60 days in total, never more than 20 idle
		f.clock = f.clock.Add(20 * 24 * time.Hour)
		next, err := f.svc.Refresh(ctx, "web", tokens.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		tokens = next
	}
}

func TestRevoke(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	tokens := f.login(t, id)
	rotated, _ := f.svc.Refresh(ctx, "web", tokens.RefreshToken)

	if err := f.svc.Revoke(ctx, tokens.RefreshToken); err != nil { // an older member revokes the family
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(ctx, "web", rotated.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("refresh after revoke err = %v", err)
	}
	if err := f.svc.Revoke(ctx, rotated.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UserForAccessToken(ctx, rotated.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("access token valid after revoke: %v", err)
	}
	if err := f.svc.Revoke(ctx, "unknown"); err != nil {
		t.Errorf("revoking an unknown token should succeed, got %v", err)
	}
}

func TestDeleteExpiredRefreshTokens(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	f.login(t, id)
	f.clock = f.clock.Add(20 * 24 * time.Hour)
	f.login(t, id)
	f.clock = f.clock.Add(15 * 24 * time.Hour) // first expired, second not
	n, err := f.svc.DeleteExpiredRefreshTokens(ctx)
	if err != nil || n != 1 {
		t.Errorf("deleted %d, %v; want 1", n, err)
	}
}
