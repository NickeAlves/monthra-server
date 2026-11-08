package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

// Policy names, used in Redis keys and metric labels.
const (
	PolicyAuthByEmail = "auth_email"
	PolicyAuthMail    = "auth_mail"
)

// DefaultRefreshGrace is how long a just-rotated refresh token answers
// ErrRefreshConflict instead of counting as reuse.
const DefaultRefreshGrace = 10 * time.Second

// Login results, the label values of auth_logins_total.
const (
	resultSuccess    = "success"
	resultFailure    = "failure"
	resultLocked     = "locked"
	resultUnverified = "unverified"
	resultCaptcha    = "captcha"
)

// Failure reasons stored in audit entries.
const (
	reasonUnknownUser   = "unknown_user"
	reasonWrongPassword = "wrong_password"
)

const maxUserAgent = 256

// maxPasswordBytes caps what login hashes: 1024 characters of up to 4 bytes.
const maxPasswordBytes = 4 * domain.MaxPasswordLength

// Options tunes the Service.
type Options struct {
	RefreshTTL    time.Duration
	EmailTokenTTL time.Duration
	ResetTokenTTL time.Duration
	// AppURL is the web app base URL for email links.
	AppURL  config.PublicURL
	Lockout core.LockoutPolicy
	// RefreshGrace defaults to DefaultRefreshGrace; a negative value disables it.
	RefreshGrace time.Duration
	Logger       *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Service runs the authentication flows.
type Service struct {
	Deps
	o Options
}

// NewService returns the service.
func NewService(d Deps, o Options) *Service {
	switch {
	case o.RefreshGrace == 0:
		o.RefreshGrace = DefaultRefreshGrace
	case o.RefreshGrace < 0:
		o.RefreshGrace = 0
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{Deps: d, o: o}
}

// Client describes where a request comes from.
type Client struct {
	IP        netip.Addr
	UserAgent string
	// Locale is the response language, used for emails before the account has one.
	Locale i18n.Locale
}

func (c Client) ipSubject() string { return ratelimit.IPSubject(c.IP) }

func (c Client) ip() string {
	if !c.IP.IsValid() {
		return ""
	}
	return c.IP.String()
}

// Tokens are the credentials handed to a client after login or refresh.
type Tokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// RegisterInput is a sign-up request.
type RegisterInput struct {
	Name         string
	Email        string
	Password     string
	CaptchaToken string
}

// Register creates an unverified account and emails a verification link.
// The outcome looks the same whether or not the address was taken: the owner
// of a taken address gets a "someone tried to sign up" email instead, and the
// stored password is never touched (no pre-hijacking).
func (s *Service) Register(ctx context.Context, in RegisterInput, c Client) error {
	var v domain.Validation
	name, err := domain.NormalizeName(in.Name)
	v.Check("name", err)
	addr, err := domain.NormalizeEmail(in.Email)
	v.Check("email", err)
	v.Check("password", domain.ValidatePassword(in.Password))
	if err := v.Err(); err != nil {
		return err
	}
	if err := s.takeEmailBudget(ctx, addr); err != nil {
		return err
	}
	if err := s.takeMailBudget(ctx, addr); err != nil {
		return err
	}
	if s.Captcha.Enabled() {
		n, err := s.Lockout.CountRegistration(ctx, c.ipSubject())
		if err != nil {
			return unavailable(err)
		}
		if n > s.o.Lockout.RegisterCaptchaAfter {
			if err := s.checkCaptcha(ctx, in.CaptchaToken, c); err != nil {
				return err
			}
		}
	}
	if s.breached(ctx, in.Password) {
		v.Add("password", domain.CodeBreached)
		return v.Err()
	}

	// Hash before looking at the address, so both outcomes cost the same.
	hash, err := s.Hasher.Hash(ctx, in.Password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user, err := s.Users.Create(ctx, domain.NewUser(name, addr, hash, c.Locale, s.o.Now()))
	if errors.Is(err, domain.ErrEmailTaken) {
		s.Mailer.Send(ctx, email.Message{To: addr, Locale: c.Locale, Template: email.TemplateRegisterAttempt, Link: s.o.AppURL.Join("/forgot-password")})
		return nil
	}
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	s.record(ctx, audit.EventRegister, user.ID, "", c, "")
	return s.sendVerification(ctx, user, c.Locale)
}

// VerifyEmail confirms the address behind a verification token.
func (s *Service) VerifyEmail(ctx context.Context, token string, c Client) error {
	if !core.ValidOpaqueToken(token) {
		return ErrTokenInvalid
	}
	now := s.o.Now()
	t, err := s.AuthTokens.Consume(ctx, core.HashToken(token), domain.PurposeVerifyEmail, now)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("consume verification token: %w", err)
	}
	if err := s.Users.MarkEmailVerified(ctx, t.UserID, now); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("mark email verified: %w", err)
	}
	s.record(ctx, audit.EventEmailVerified, t.UserID, "", c, "")
	return nil
}

// ResendVerification emails a new verification link when the address
// belongs to an unverified account, and silently does nothing otherwise.
func (s *Service) ResendVerification(ctx context.Context, rawEmail string, c Client) error {
	addr, err := domain.NormalizeEmail(rawEmail)
	if err != nil {
		var v domain.Validation
		v.Check("email", err)
		return v.Err()
	}
	if err := s.takeEmailBudget(ctx, addr); err != nil {
		return err
	}
	if err := s.takeMailBudget(ctx, addr); err != nil {
		return err
	}
	u, err := s.Users.ByEmail(ctx, addr)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if u.EmailVerified() {
		return nil
	}
	if err := s.AuthTokens.InvalidateFor(ctx, u.ID, domain.PurposeVerifyEmail, s.o.Now()); err != nil {
		return fmt.Errorf("invalidate verification tokens: %w", err)
	}
	return s.sendVerification(ctx, u, u.Preferences.Locale)
}

// LoginInput is a password login.
type LoginInput struct {
	Email        string
	Password     string
	CaptchaToken string
}

// Login checks a password and opens a session. Unknown addresses and wrong
// passwords get the same answer after the same work.
func (s *Service) Login(ctx context.Context, in LoginInput, c Client) (Tokens, error) {
	addr, err := domain.NormalizeEmail(in.Email)
	if err != nil || in.Password == "" || len(in.Password) > maxPasswordBytes {
		s.Metrics.Login(resultFailure)
		return Tokens{}, ErrInvalidCredentials
	}
	if err := s.takeEmailBudget(ctx, addr); err != nil {
		return Tokens{}, err
	}
	emailKey, ip := core.HashIdentifier("email", addr), c.ipSubject()
	state, err := s.Lockout.Check(ctx, emailKey, ip)
	if err != nil {
		return Tokens{}, unavailable(err)
	}
	if state.RetryAfter > 0 {
		s.Metrics.Login(resultLocked)
		return Tokens{}, &RateLimitedError{RetryAfter: state.RetryAfter}
	}
	if s.Captcha.Enabled() && s.o.Lockout.NeedsCaptcha(state) {
		if err := s.checkCaptcha(ctx, in.CaptchaToken, c); err != nil {
			s.Metrics.Login(resultCaptcha)
			return Tokens{}, err
		}
	}

	u, err := s.Users.ByEmail(ctx, addr)
	if errors.Is(err, domain.ErrNotFound) {
		s.Hasher.VerifyDummy(ctx, in.Password)
		return Tokens{}, s.loginFailed(ctx, emailKey, ip, "", c, reasonUnknownUser)
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("find user: %w", err)
	}
	ok, rehash, err := s.Hasher.Verify(ctx, in.Password, u.PasswordHash)
	if err != nil {
		return Tokens{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return Tokens{}, s.loginFailed(ctx, emailKey, ip, u.ID, c, reasonWrongPassword)
	}
	if !u.EmailVerified() {
		s.Metrics.Login(resultUnverified)
		return Tokens{}, ErrEmailNotVerified
	}
	if rehash {
		s.rehash(ctx, u.ID, in.Password)
	}
	if err := s.Lockout.Reset(ctx, emailKey); err != nil {
		s.o.Logger.WarnContext(ctx, "reset login lockout", slog.String("error", err.Error()))
	}
	tokens, sess, err := s.startSession(ctx, u.ID, c)
	if err != nil {
		return Tokens{}, err
	}
	s.record(ctx, audit.EventLoginSuccess, u.ID, sess.ID, c, "")
	s.Metrics.Login(resultSuccess)
	return tokens, nil
}

// Refresh rotates a refresh token: the presented token is spent and a new
// pair is issued. Presenting a spent token again, after the grace window,
// is treated as theft: the whole session is revoked.
func (s *Service) Refresh(ctx context.Context, token string, c Client) (Tokens, error) {
	if !core.ValidOpaqueToken(token) {
		return Tokens{}, ErrUnauthorized
	}
	now := s.o.Now()
	hash := core.HashToken(token)
	rt, err := s.RefreshTokens.Consume(ctx, hash, now)
	if errors.Is(err, domain.ErrNotFound) {
		return Tokens{}, s.refreshRejected(ctx, hash, now, c)
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("consume refresh token: %w", err)
	}
	sess, err := s.Sessions.ByID(ctx, rt.SessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return Tokens{}, ErrUnauthorized
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("find session: %w", err)
	}
	if !sess.Active(now) {
		return Tokens{}, ErrUnauthorized
	}
	tokens, err := s.issue(ctx, rt.UserID, sess.ID, now)
	if err != nil {
		return Tokens{}, err
	}
	if err := s.Sessions.Touch(ctx, sess.ID, now, tokens.RefreshExpiresAt); err != nil {
		return Tokens{}, fmt.Errorf("touch session: %w", err)
	}
	return tokens, nil
}

// Logout ends the caller's session and denylists the access token it used.
func (s *Service) Logout(ctx context.Context, p core.Principal, c Client) error {
	now := s.o.Now()
	if err := s.revokeSession(ctx, p.SessionID, now); err != nil {
		return err
	}
	if err := s.Revocations.DenyToken(ctx, p.TokenID, p.ExpiresAt.Sub(now)+core.ClockLeeway); err != nil {
		return unavailable(err)
	}
	s.record(ctx, audit.EventLogout, p.UserID, p.SessionID, c, "")
	return nil
}

func (s *Service) refreshRejected(ctx context.Context, hash string, now time.Time, c Client) error {
	old, err := s.RefreshTokens.ByHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("find refresh token: %w", err)
	}
	if old.UsedAt == nil {
		return ErrUnauthorized // expired before it was used
	}
	sess, err := s.Sessions.ByID(ctx, old.SessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("find session: %w", err)
	}
	if !sess.Active(now) {
		return ErrUnauthorized // already revoked (or expired): nothing left to protect
	}
	if s.o.RefreshGrace > 0 && now.Sub(*old.UsedAt) <= s.o.RefreshGrace {
		return ErrRefreshConflict
	}
	if err := s.revokeSession(ctx, old.SessionID, now); err != nil {
		return err
	}
	s.Metrics.RefreshReuse()
	s.record(ctx, audit.EventRefreshReuseDetected, old.UserID, old.SessionID, c, "")
	s.o.Logger.WarnContext(ctx, "refresh token reuse detected, session revoked", slog.String("session_id", old.SessionID))
	return ErrUnauthorized
}

func (s *Service) revokeSession(ctx context.Context, sessionID string, now time.Time) error {
	if err := s.Sessions.Revoke(ctx, sessionID, now); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("revoke session: %w", err)
	}
	// Access tokens of the session stay valid for at most their TTL: deny the
	// whole session in Redis for that long.
	if err := s.Revocations.RevokeSession(ctx, sessionID, s.Tokens.TTL()+core.ClockLeeway); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Service) startSession(ctx context.Context, userID string, c Client) (Tokens, domain.Session, error) {
	now := s.o.Now()
	sess, err := s.Sessions.Create(ctx, domain.Session{
		UserID: userID, UserAgent: truncate(c.UserAgent, maxUserAgent), IP: c.ip(),
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(s.o.RefreshTTL),
	})
	if err != nil {
		return Tokens{}, domain.Session{}, fmt.Errorf("create session: %w", err)
	}
	tokens, err := s.issue(ctx, userID, sess.ID, now)
	if err != nil {
		return Tokens{}, domain.Session{}, err
	}
	return tokens, sess, nil
}

func (s *Service) issue(ctx context.Context, userID, sessionID string, now time.Time) (Tokens, error) {
	refresh := core.NewOpaqueToken()
	refreshExp := now.Add(s.o.RefreshTTL)
	if err := s.RefreshTokens.Create(ctx, domain.RefreshToken{
		TokenHash: core.HashToken(refresh), SessionID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: refreshExp,
	}); err != nil {
		return Tokens{}, fmt.Errorf("store refresh token: %w", err)
	}
	at, err := s.Tokens.Issue(userID, sessionID)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: at.Token, AccessExpiresAt: at.ExpiresAt, RefreshToken: refresh, RefreshExpiresAt: refreshExp}, nil
}

func (s *Service) sendVerification(ctx context.Context, u domain.User, locale i18n.Locale) error {
	return s.sendTokenLink(ctx, tokenLink{
		user: u, locale: locale, purpose: domain.PurposeVerifyEmail, ttl: s.o.EmailTokenTTL,
		template: email.TemplateVerifyEmail, path: "/verify-email",
	})
}

// tokenLink is an email carrying a single-use token.
type tokenLink struct {
	user     domain.User
	locale   i18n.Locale
	purpose  domain.TokenPurpose
	ttl      time.Duration
	template email.Template
	path     string
}

func (s *Service) sendTokenLink(ctx context.Context, l tokenLink) error {
	token := core.NewOpaqueToken()
	now := s.o.Now()
	if err := s.AuthTokens.Create(ctx, domain.AuthToken{
		TokenHash: core.HashToken(token), UserID: l.user.ID, Purpose: l.purpose,
		CreatedAt: now, ExpiresAt: now.Add(l.ttl),
	}); err != nil {
		return fmt.Errorf("store %s token: %w", l.purpose, err)
	}
	// The token travels in the fragment, which browsers never send to servers
	// or put in Referer headers, so it stays out of access logs.
	s.Mailer.Send(ctx, email.Message{
		To: l.user.Email, Locale: l.locale, Template: l.template,
		Link: s.o.AppURL.Join(l.path + "#token=" + token), LinkTTL: l.ttl,
	})
	return nil
}

func (s *Service) loginFailed(ctx context.Context, emailKey, ip, userID string, c Client, reason string) error {
	if _, err := s.Lockout.RecordFailure(ctx, emailKey, ip); err != nil {
		s.o.Logger.ErrorContext(ctx, "record login failure", slog.String("error", err.Error()))
	}
	s.record(ctx, audit.EventLoginFailure, userID, "", c, reason)
	s.Metrics.Login(resultFailure)
	return ErrInvalidCredentials
}

func (s *Service) rehash(ctx context.Context, userID, password string) {
	hash, err := s.Hasher.Hash(ctx, password)
	if err == nil {
		err = s.Users.UpdatePasswordHash(ctx, userID, hash, s.o.Now())
	}
	if err != nil {
		s.o.Logger.WarnContext(ctx, "upgrade password hash", slog.String("error", err.Error()))
	}
}

func (s *Service) takeEmailBudget(ctx context.Context, addr string) error {
	res, err := s.EmailLimit.Take(ctx, core.HashIdentifier("email", addr))
	if err != nil {
		return unavailable(err)
	}
	if !res.Allowed {
		s.Metrics.RateLimitRejected(PolicyAuthByEmail)
		return &RateLimitedError{RetryAfter: res.RetryAfter}
	}
	return nil
}

// takeMailBudget caps the account emails sent to addr. It runs before the
// address is looked up, so a 429 says nothing about whether it has an account.
func (s *Service) takeMailBudget(ctx context.Context, addr string) error {
	res, err := s.MailLimit.Take(ctx, core.HashIdentifier("email", addr))
	if err != nil {
		return unavailable(err)
	}
	if !res.Allowed {
		s.Metrics.RateLimitRejected(PolicyAuthMail)
		return &RateLimitedError{RetryAfter: res.RetryAfter}
	}
	return nil
}

func (s *Service) checkCaptcha(ctx context.Context, token string, c Client) error {
	if token == "" {
		return ErrCaptchaRequired
	}
	ok, err := s.Captcha.Verify(ctx, token, c.ip())
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		return ErrCaptchaInvalid
	}
	return nil
}

// breached fails open: when Have I Been Pwned is down, sign-up goes on.
func (s *Service) breached(ctx context.Context, password string) bool {
	breached, err := s.Breach.Breached(ctx, password)
	if err != nil {
		s.o.Logger.WarnContext(ctx, "password breach check unavailable, skipping it", slog.String("error", err.Error()))
		return false
	}
	return breached
}

func (s *Service) record(ctx context.Context, event audit.Event, userID, sessionID string, c Client, reason string) {
	s.Auditor.Record(ctx, audit.Entry{Event: event, UserID: userID, SessionID: sessionID, IP: c.ip(), UserAgent: c.UserAgent, Reason: reason})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
