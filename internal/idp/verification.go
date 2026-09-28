package idp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/danilopavk/alphabeter/internal/email"
)

// Email verification and password reset. Both send a link with a random
// token; only the token's hash is stored, in Redis, with a TTL.
//
// Verification links stay valid until they expire (email scanners open links
// before people do). Reset links only show a form; the reset itself is a POST,
// and each link is tied to the password hash it was issued for, so any reset
// kills every older link.

func validatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		return ErrWeakPassword
	}
	return nil
}

// mailAllowed counts an email request against the per-address and per-IP
// limits, whether or not the address has an account.
func (s *Service) mailAllowed(ctx context.Context, address, ip string) error {
	addrKey := s.cfg.KeyPrefix + "rl:mail:addr:" + address
	ipKey := s.cfg.KeyPrefix + "rl:mail:ip:" + ip
	pipe := s.rdb.TxPipeline()
	addrCount := pipe.Incr(ctx, addrKey)
	pipe.ExpireNX(ctx, addrKey, s.cfg.MailWindow)
	ipCount := pipe.Incr(ctx, ipKey)
	pipe.ExpireNX(ctx, ipKey, s.cfg.MailWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if addrCount.Val() > int64(s.cfg.MailLimit) || ipCount.Val() > int64(s.cfg.MailLimitPerIP) {
		return ErrRateLimited
	}
	return nil
}

func (s *Service) link(path, token string) string {
	return s.cfg.PublicURL + path + "?token=" + token
}

// SendVerification emails a verification link if the address belongs to an
// unverified account. Otherwise it does nothing, and says nothing.
func (s *Service) SendVerification(ctx context.Context, address, ip string) error {
	address = normalizeEmail(address)
	if err := s.mailAllowed(ctx, address, ip); err != nil {
		return err
	}
	var (
		id       int64
		verified bool
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, email_verified_at IS NOT NULL FROM users WHERE email = $1`, address,
	).Scan(&id, &verified)
	if errors.Is(err, pgx.ErrNoRows) || verified {
		return nil
	}
	if err != nil {
		return err
	}
	token := randomToken()
	if err := s.rdb.Set(ctx, s.key("ev:", token), id, s.cfg.VerifyTTL).Err(); err != nil {
		return err
	}
	return s.cfg.Mailer.Send(ctx, message(s.cfg.MailFrom, address, "Verify your email for Alphabeter",
		"Confirm your email address to start using Alphabeter:",
		s.link("/oauth/verify", token), "Verify my email",
		fmt.Sprintf("The link works for %s. If you didn't sign up, ignore this email.", hours(s.cfg.VerifyTTL))))
}

// VerifyEmail marks the account behind a verification link as verified.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	v, err := s.rdb.Get(ctx, s.key("ev:", token)).Result()
	if errors.Is(err, redis.Nil) {
		return ErrInvalidLink
	}
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, $2) WHERE id = $1`, id, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidLink // the account was deleted
	}
	return nil
}

type resetData struct {
	UserID      int64  `json:"u"`
	Fingerprint string `json:"f"` // of the password hash the link was issued for
}

func fingerprint(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:8])
}

// SendPasswordReset emails a reset link if the address has an account, and
// says nothing either way.
func (s *Service) SendPasswordReset(ctx context.Context, address, ip string) error {
	address = normalizeEmail(address)
	if err := s.mailAllowed(ctx, address, ip); err != nil {
		return err
	}
	var (
		id   int64
		hash string
	)
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE email = $1`, address).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(resetData{UserID: id, Fingerprint: fingerprint(hash)})
	if err != nil {
		return err
	}
	token := randomToken()
	if err := s.rdb.Set(ctx, s.key("pr:", token), data, s.cfg.ResetTTL).Err(); err != nil {
		return err
	}
	return s.cfg.Mailer.Send(ctx, message(s.cfg.MailFrom, address, "Reset your Alphabeter password",
		"Someone asked to reset the password of your Alphabeter account. To choose a new one, open:",
		s.link("/oauth/reset/confirm", token), "Choose a new password",
		fmt.Sprintf("The link works for %s. If it wasn't you, ignore this email: your password stays as it is.", hours(s.cfg.ResetTTL))))
}

// CheckResetLink reports whether a reset link can still be used.
func (s *Service) CheckResetLink(ctx context.Context, token string) error {
	_, _, err := s.resetTarget(ctx, token)
	return err
}

func (s *Service) resetTarget(ctx context.Context, token string) (int64, string, error) {
	raw, err := s.rdb.Get(ctx, s.key("pr:", token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return 0, "", ErrInvalidLink
	}
	if err != nil {
		return 0, "", err
	}
	var data resetData
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, "", err
	}
	var hash string
	err = s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, data.UserID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrInvalidLink
	}
	if err != nil {
		return 0, "", err
	}
	if fingerprint(hash) != data.Fingerprint {
		return 0, "", ErrInvalidLink // the password changed since the link was sent
	}
	return data.UserID, hash, nil
}

// ResetPassword sets a new password through a reset link. It also verifies the
// email (the link proved the parent reads it) and logs out every session.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	id, oldHash, err := s.resetTarget(ctx, token)
	if err != nil {
		return err
	}
	newHash, err := hashPassword(password)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := s.now()
	var address string
	err = tx.QueryRow(ctx,
		`UPDATE users SET password_hash = $1, email_verified_at = COALESCE(email_verified_at, $2)
		 WHERE id = $3 AND password_hash = $4 RETURNING email`,
		newHash, now, id, oldHash,
	).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidLink // a concurrent reset won
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`, now, id,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.rdb.Del(ctx, s.key("pr:", token), s.cfg.KeyPrefix+"rl:email:"+address)
	return nil
}

func hours(d time.Duration) string {
	if h := int(d.Hours()); h != 1 {
		return fmt.Sprintf("%d hours", h)
	}
	return "1 hour"
}

func message(from, to, subject, intro, link, button, outro string) email.Message {
	return email.Message{
		From:    from,
		To:      to,
		Subject: subject,
		Text:    fmt.Sprintf("Hi!\n\n%s\n\n%s\n\n%s\n\nAlphabeter\n", intro, link, outro),
		HTML: fmt.Sprintf(`<p>Hi!</p><p>%s</p><p><a href="%s">%s</a></p><p>%s</p><p>Alphabeter</p>`,
			html.EscapeString(intro), html.EscapeString(link), html.EscapeString(button), html.EscapeString(outro)),
	}
}
