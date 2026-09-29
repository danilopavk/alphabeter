// Package idp is Alphabeter's own identity provider: email/password accounts
// and an OAuth 2.1 authorization server (authorization code + PKCE, rotating
// refresh tokens).
//
// Every token is 32 random bytes, base64url-encoded; only SHA-256 hashes are
// stored. Auth codes and access tokens live in Redis with a TTL; refresh tokens
// live in Postgres.
package idp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danilopavk/alphabeter/internal/email"
)

var (
	ErrInvalidEmail       = errors.New("idp: invalid email address")
	ErrWeakPassword       = errors.New("idp: password must be 8 to 128 characters")
	ErrEmailTaken         = errors.New("idp: email already registered")
	ErrInvalidCredentials = errors.New("idp: invalid email or password")
	ErrRateLimited        = errors.New("idp: too many attempts")
	ErrInvalidClient      = errors.New("idp: unknown client")
	ErrInvalidRedirect    = errors.New("idp: redirect_uri not registered for client")
	ErrInvalidChallenge   = errors.New("idp: code_challenge must be an S256 challenge")
	ErrInvalidGrant       = errors.New("idp: invalid grant")
	ErrInvalidToken       = errors.New("idp: invalid or expired token")
	ErrEmailNotVerified   = errors.New("idp: email address not verified")
	ErrInvalidLink        = errors.New("idp: link is invalid or has expired")
)

// Client is a registered OAuth client. All clients are public and must use PKCE.
type Client struct {
	ID           string
	RedirectURIs []string // compared exactly
}

type Config struct {
	Clients     []Client
	KeyPrefix   string        // prefix for every Redis key
	AccessTTL   time.Duration // default 15 min
	RefreshTTL  time.Duration // default 30 days, renewed on every rotation
	CodeTTL     time.Duration // default 60 s
	LoginLimit  int           // failed-or-not login attempts per email and per IP; default 10
	LoginWindow time.Duration // default 15 min

	PublicURL      string        // for links in emails, e.g. https://alphabeter.example
	MailFrom       string        // "Alphabeter <no-reply@alphabeter.example>"
	Mailer         email.Sender  // default: email.Log (prints instead of sending)
	MailLimit      int           // emails per address per MailWindow; default 5
	MailLimitPerIP int           // emails requested per IP per MailWindow; default 20
	MailWindow     time.Duration // default 1 h
	VerifyTTL      time.Duration // verification links; default 24 h
	ResetTTL       time.Duration // password reset links; default 1 h
}

type Tokens struct {
	UserID       int64
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration // of the access token
}

type Service struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	cfg  Config
	now  func() time.Time
}

func NewService(pool *pgxpool.Pool, rdb *redis.Client, cfg Config) *Service {
	cfg.AccessTTL = cmpOr(cfg.AccessTTL, 15*time.Minute)
	cfg.RefreshTTL = cmpOr(cfg.RefreshTTL, 30*24*time.Hour)
	cfg.CodeTTL = cmpOr(cfg.CodeTTL, time.Minute)
	cfg.LoginLimit = cmpOr(cfg.LoginLimit, 10)
	cfg.LoginWindow = cmpOr(cfg.LoginWindow, 15*time.Minute)
	if cfg.Mailer == nil {
		cfg.Mailer = email.Log{}
	}
	cfg.MailLimit = cmpOr(cfg.MailLimit, 5)
	cfg.MailLimitPerIP = cmpOr(cfg.MailLimitPerIP, 20)
	cfg.MailWindow = cmpOr(cfg.MailWindow, time.Hour)
	cfg.VerifyTTL = cmpOr(cfg.VerifyTTL, 24*time.Hour)
	cfg.ResetTTL = cmpOr(cfg.ResetTTL, time.Hour)
	return &Service{pool: pool, rdb: rdb, cfg: cfg, now: time.Now}
}

func cmpOr[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

func (s *Service) AccessTTL() time.Duration  { return s.cfg.AccessTTL }
func (s *Service) RefreshTTL() time.Duration { return s.cfg.RefreshTTL }

// --- accounts ---

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func (s *Service) Register(ctx context.Context, email, password string) (int64, error) {
	email = normalizeEmail(email)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return 0, ErrInvalidEmail
	}
	if err := validatePassword(password); err != nil {
		return 0, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`, email, hash,
	).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return 0, ErrEmailTaken
	}
	return id, err
}

// Authenticate checks an email and password. Attempts are limited per email
// and per client IP.
func (s *Service) Authenticate(ctx context.Context, email, password, ip string) (int64, error) {
	email = normalizeEmail(email)
	emailKey := s.cfg.KeyPrefix + "rl:email:" + email
	ipKey := s.cfg.KeyPrefix + "rl:ip:" + ip
	pipe := s.rdb.TxPipeline()
	emailCount := pipe.Incr(ctx, emailKey)
	pipe.ExpireNX(ctx, emailKey, s.cfg.LoginWindow)
	ipCount := pipe.Incr(ctx, ipKey)
	pipe.ExpireNX(ctx, ipKey, s.cfg.LoginWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	if limit := int64(s.cfg.LoginLimit); emailCount.Val() > limit || ipCount.Val() > limit {
		return 0, ErrRateLimited
	}

	var (
		id       int64
		hash     string
		verified bool
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash, email_verified_at IS NOT NULL FROM users WHERE email = $1`, email,
	).Scan(&id, &hash, &verified)
	if errors.Is(err, pgx.ErrNoRows) {
		verifyPassword(dummyHash(), password)
		return 0, ErrInvalidCredentials
	}
	if err != nil {
		return 0, err
	}
	if !verifyPassword(hash, password) {
		return 0, ErrInvalidCredentials
	}
	// Only reported after a correct password, so it reveals nothing to guessers.
	if !verified {
		return 0, ErrEmailNotVerified
	}
	s.rdb.Del(ctx, emailKey)
	return id, nil
}

// DeleteAccount deletes a user once their password checks out; attempts count
// against the login limits. Their kids, progress and refresh tokens go with
// them (ON DELETE CASCADE). Access tokens already issued stay valid until they
// expire, as after a password reset.
func (s *Service) DeleteAccount(ctx context.Context, userID int64, password, ip string) error {
	var address string
	err := s.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	if _, err := s.Authenticate(ctx, address, password, ip); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	return err
}

// --- authorization codes ---

// CheckClient validates client_id and redirect_uri of an authorization request.
func (s *Service) CheckClient(clientID, redirectURI string) error {
	i := slices.IndexFunc(s.cfg.Clients, func(c Client) bool { return c.ID == clientID })
	if i < 0 {
		return ErrInvalidClient
	}
	if !slices.Contains(s.cfg.Clients[i].RedirectURIs, redirectURI) {
		return ErrInvalidRedirect
	}
	return nil
}

func (s *Service) KnownClient(clientID string) bool {
	return slices.ContainsFunc(s.cfg.Clients, func(c Client) bool { return c.ID == clientID })
}

// ValidChallenge reports whether c looks like an S256 code challenge
// (base64url without padding of a SHA-256 digest).
func ValidChallenge(c string) bool {
	return len(c) == 43 && isBase64URL(c)
}

func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !isUnreserved(r) {
			return false
		}
	}
	return true
}

func isBase64URL(s string) bool {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isUnreserved(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)
}

type codeData struct {
	UserID      int64  `json:"u"`
	ClientID    string `json:"c"`
	RedirectURI string `json:"r"`
	Challenge   string `json:"p"`
}

func (s *Service) IssueCode(ctx context.Context, userID int64, clientID, redirectURI, challenge string) (string, error) {
	if err := s.CheckClient(clientID, redirectURI); err != nil {
		return "", err
	}
	if !ValidChallenge(challenge) {
		return "", ErrInvalidChallenge
	}
	var verified bool
	if err := s.pool.QueryRow(ctx,
		`SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1`, userID,
	).Scan(&verified); err != nil {
		return "", err
	}
	if !verified {
		return "", ErrEmailNotVerified
	}
	data, err := json.Marshal(codeData{userID, clientID, redirectURI, challenge})
	if err != nil {
		return "", err
	}
	code := randomToken()
	if err := s.rdb.Set(ctx, s.key("ac:", code), data, s.cfg.CodeTTL).Err(); err != nil {
		return "", err
	}
	return code, nil
}

// ExchangeCode redeems an authorization code. A code works once, even if the
// exchange fails.
func (s *Service) ExchangeCode(ctx context.Context, clientID, code, redirectURI, verifier string) (Tokens, error) {
	raw, err := s.rdb.GetDel(ctx, s.key("ac:", code)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Tokens{}, ErrInvalidGrant
	}
	if err != nil {
		return Tokens{}, err
	}
	var data codeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return Tokens{}, err
	}
	if data.ClientID != clientID || data.RedirectURI != redirectURI || !validVerifier(verifier) {
		return Tokens{}, ErrInvalidGrant
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(data.Challenge)) != 1 {
		return Tokens{}, ErrInvalidGrant
	}

	var family [16]byte
	rand.Read(family[:])
	family[6] = family[6]&0x0f | 0x40 // UUID version 4
	family[8] = family[8]&0x3f | 0x80 // RFC 4122 variant
	refresh, err := s.insertRefresh(ctx, s.pool, data.UserID, clientID, family)
	if err != nil {
		return Tokens{}, err
	}
	return s.withAccessToken(ctx, data.UserID, refresh)
}

// --- refresh and access tokens ---

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (s *Service) insertRefresh(ctx context.Context, db execer, userID int64, clientID string, family [16]byte) (string, error) {
	token := randomToken()
	_, err := db.Exec(ctx,
		`INSERT INTO refresh_tokens (token_hash, user_id, client_id, family_id, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		hashToken(token), userID, clientID, family, s.now(), s.now().Add(s.cfg.RefreshTTL))
	return token, err
}

func (s *Service) withAccessToken(ctx context.Context, userID int64, refresh string) (Tokens, error) {
	access := randomToken()
	if err := s.rdb.Set(ctx, s.key("at:", access), userID, s.cfg.AccessTTL).Err(); err != nil {
		return Tokens{}, err
	}
	return Tokens{UserID: userID, AccessToken: access, RefreshToken: refresh, ExpiresIn: s.cfg.AccessTTL}, nil
}

// Refresh rotates a refresh token. Presenting a token that was already rotated
// means it leaked (or raced): the whole family is revoked.
func (s *Service) Refresh(ctx context.Context, clientID, token string) (Tokens, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback(ctx)

	var (
		userID          int64
		tokenClient     string
		family          [16]byte
		expiresAt       time.Time
		usedAt, revoked *time.Time
		verified        bool
	)
	err = tx.QueryRow(ctx,
		`SELECT rt.user_id, rt.client_id, rt.family_id, rt.expires_at, rt.used_at, rt.revoked_at,
		        u.email_verified_at IS NOT NULL
		 FROM refresh_tokens rt JOIN users u ON u.id = rt.user_id
		 WHERE rt.token_hash = $1 FOR UPDATE OF rt`, hashToken(token),
	).Scan(&userID, &tokenClient, &family, &expiresAt, &usedAt, &revoked, &verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrInvalidGrant
	}
	if err != nil {
		return Tokens{}, err
	}
	now := s.now()
	switch {
	case revoked != nil:
		return Tokens{}, ErrInvalidGrant
	case usedAt != nil:
		if _, err := tx.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = $1 WHERE family_id = $2 AND revoked_at IS NULL`, now, family,
		); err != nil {
			return Tokens{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Tokens{}, err
		}
		return Tokens{}, ErrInvalidGrant
	case tokenClient != clientID, !now.Before(expiresAt), !verified:
		return Tokens{}, ErrInvalidGrant
	}

	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET used_at = $1 WHERE token_hash = $2`, now, hashToken(token)); err != nil {
		return Tokens{}, err
	}
	next, err := s.insertRefresh(ctx, tx, userID, clientID, family)
	if err != nil {
		return Tokens{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tokens{}, err
	}
	return s.withAccessToken(ctx, userID, next)
}

// Revoke invalidates an access token or a refresh token's whole family
// (RFC 7009). Unknown tokens are not an error.
func (s *Service) Revoke(ctx context.Context, token string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $1
		 WHERE family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $2) AND revoked_at IS NULL`,
		s.now(), hashToken(token),
	); err != nil {
		return err
	}
	return s.rdb.Del(ctx, s.key("at:", token)).Err()
}

func (s *Service) UserForAccessToken(ctx context.Context, token string) (int64, error) {
	v, err := s.rdb.Get(ctx, s.key("at:", token)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, ErrInvalidToken
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (s *Service) DeleteExpiredRefreshTokens(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < $1`, s.now())
	return tag.RowsAffected(), err
}

// --- helpers ---

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Service) key(kind, token string) string {
	return s.cfg.KeyPrefix + kind + hex.EncodeToString(hashToken(token))
}
