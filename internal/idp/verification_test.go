package idp

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var linkToken = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

// tokenFrom returns the token of the link in the last email sent to the address.
func (f *fixture) tokenFrom(t *testing.T, address, path string) string {
	t.Helper()
	m, ok := f.mail.Last(address)
	if !ok {
		t.Fatalf("no email sent to %s", address)
	}
	link := "https://alphabeter.test" + path + "?token="
	if !strings.Contains(m.Text, link) || !strings.Contains(m.HTML, link) {
		t.Fatalf("email lacks a %s link:\ntext: %s\nhtml: %s", path, m.Text, m.HTML)
	}
	return linkToken.FindStringSubmatch(m.Text)[1]
}

func TestNewAccountsMustVerifyTheirEmail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, err := f.svc.Register(ctx, "new@example.com", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(ctx, "new@example.com", "correct horse", "10.0.0.1"); !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("right password, unverified: err = %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, "new@example.com", "wrong password", "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password, unverified: err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := f.svc.IssueCode(ctx, id, "web", testRedirect, challengeFor(testVerifier)); !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("IssueCode for an unverified account: err = %v", err)
	}
}

func TestVerificationEmail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.svc.Register(ctx, "new@example.com", "correct horse")

	if err := f.svc.SendVerification(ctx, "New@Example.com", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	m, _ := f.mail.Last("new@example.com")
	if m.From != "Alphabeter <no-reply@alphabeter.test>" || !strings.Contains(m.Subject, "Verify") {
		t.Errorf("email from %q, subject %q", m.From, m.Subject)
	}
	token := f.tokenFrom(t, "new@example.com", "/oauth/verify")

	if err := f.svc.VerifyEmail(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(ctx, "new@example.com", "correct horse", "10.0.0.1"); err != nil {
		t.Errorf("after verifying: %v", err)
	}
	// Email scanners open links before people do, so a link keeps working.
	if err := f.svc.VerifyEmail(ctx, token); err != nil {
		t.Errorf("second use of the link: %v", err)
	}
	if err := f.svc.VerifyEmail(ctx, "not-a-token"); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("unknown token: err = %v", err)
	}
	if ttl := f.svc.rdb.TTL(ctx, f.svc.key("ev:", token)).Val(); ttl <= 23*time.Hour || ttl > 24*time.Hour {
		t.Errorf("verification link TTL = %s", ttl)
	}
}

func TestSendVerificationOnlyEmailsUnverifiedAccounts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.register(t, "verified@example.com")
	for _, address := range []string{"verified@example.com", "nobody@example.com"} {
		if err := f.svc.SendVerification(ctx, address, "10.0.0.1"); err != nil {
			t.Errorf("%s: err = %v", address, err)
		}
		if _, sent := f.mail.Last(address); sent {
			t.Errorf("%s: an email was sent", address)
		}
	}
}

func TestEmailsAreRateLimited(t *testing.T) {
	f := setup(t)
	f.svc.cfg.MailLimit = 2
	ctx := context.Background()
	f.svc.Register(ctx, "new@example.com", "correct horse")
	f.svc.SendVerification(ctx, "new@example.com", "10.0.0.1")
	f.svc.SendPasswordReset(ctx, "new@example.com", "10.0.0.2") // the limit covers both kinds
	if err := f.svc.SendVerification(ctx, "new@example.com", "10.0.0.3"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third email to one address: err = %v", err)
	}

	f.svc.cfg.MailLimit = 100
	f.svc.cfg.MailLimitPerIP = 2
	for _, address := range []string{"a@example.com", "b@example.com"} {
		f.svc.SendPasswordReset(ctx, address, "10.0.0.9")
	}
	if err := f.svc.SendPasswordReset(ctx, "c@example.com", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third email from one IP: err = %v", err)
	}
}

func TestPasswordReset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	session := f.login(t, id)

	if err := f.svc.SendPasswordReset(ctx, "parent@example.com", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	m, _ := f.mail.Last("parent@example.com")
	if !strings.Contains(m.Subject, "password") {
		t.Errorf("subject %q", m.Subject)
	}
	token := f.tokenFrom(t, "parent@example.com", "/oauth/reset/confirm")
	if ttl := f.svc.rdb.TTL(ctx, f.svc.key("pr:", token)).Val(); ttl <= 59*time.Minute || ttl > time.Hour {
		t.Errorf("reset link TTL = %s", ttl)
	}

	if err := f.svc.CheckResetLink(ctx, token); err != nil {
		t.Errorf("fresh link: %v", err)
	}
	if err := f.svc.ResetPassword(ctx, token, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password: err = %v", err)
	}
	if err := f.svc.ResetPassword(ctx, token, "a new password"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if _, err := f.svc.Authenticate(ctx, "parent@example.com", "correct horse", "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if got, err := f.svc.Authenticate(ctx, "parent@example.com", "a new password", "10.0.0.1"); err != nil || got != id {
		t.Errorf("new password: %d, %v", got, err)
	}
	if err := f.svc.ResetPassword(ctx, token, "yet another password"); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("link reused: err = %v", err)
	}
	if err := f.svc.CheckResetLink(ctx, token); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("used link still checks out: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, "web", session.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("other sessions survive a reset: %v", err)
	}
}

func TestResetKillsOlderLinks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.register(t, "parent@example.com")
	f.svc.SendPasswordReset(ctx, "parent@example.com", "10.0.0.1")
	older := f.tokenFrom(t, "parent@example.com", "/oauth/reset/confirm")
	f.svc.SendPasswordReset(ctx, "parent@example.com", "10.0.0.1")
	newer := f.tokenFrom(t, "parent@example.com", "/oauth/reset/confirm")

	if err := f.svc.ResetPassword(ctx, newer, "a new password"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResetPassword(ctx, older, "attacker password"); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("older link after a reset: err = %v", err)
	}
}

func TestResetVerifiesTheEmail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.svc.Register(ctx, "new@example.com", "correct horse")
	f.svc.SendPasswordReset(ctx, "new@example.com", "10.0.0.1")
	if err := f.svc.ResetPassword(ctx, f.tokenFrom(t, "new@example.com", "/oauth/reset/confirm"), "a new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(ctx, "new@example.com", "a new password", "10.0.0.1"); err != nil {
		t.Errorf("after a reset the account should be verified: %v", err)
	}
}

func TestPasswordResetForUnknownEmailSendsNothing(t *testing.T) {
	f := setup(t)
	if err := f.svc.SendPasswordReset(context.Background(), "nobody@example.com", "10.0.0.1"); err != nil {
		t.Errorf("err = %v", err)
	}
	if _, sent := f.mail.Last("nobody@example.com"); sent {
		t.Error("an email was sent")
	}
}

func TestUnverifiedAccountsCannotRefresh(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.register(t, "parent@example.com")
	session := f.login(t, id)
	f.svc.pool.Exec(ctx, `UPDATE users SET email_verified_at = NULL WHERE id = $1`, id)
	if _, err := f.svc.Refresh(ctx, "web", session.RefreshToken); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("err = %v", err)
	}
}
