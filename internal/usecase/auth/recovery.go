package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
)

// ForgotPassword emails a password reset link when the address has an
// account, verified or not, and silently does nothing otherwise. Earlier
// links stop working: only the newest one is valid.
func (s *Service) ForgotPassword(ctx context.Context, rawEmail string, c Client) error {
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
	if err := s.AuthTokens.InvalidateFor(ctx, u.ID, domain.PurposeResetPassword, s.o.Now()); err != nil {
		return fmt.Errorf("invalidate reset tokens: %w", err)
	}
	if err := s.sendTokenLink(ctx, tokenLink{
		user: u, locale: u.Preferences.Locale, purpose: domain.PurposeResetPassword, ttl: s.o.ResetTokenTTL,
		template: email.TemplateResetPassword, path: "/reset-password",
	}); err != nil {
		return err
	}
	s.record(ctx, audit.EventPasswordResetRequest, u.ID, "", c, "")
	return nil
}

// ResetPasswordInput is a new password and the emailed token that allows it.
type ResetPasswordInput struct {
	Token    string
	Password string
}

// ResetPassword sets a new password through an emailed token and signs out
// every session, since whoever held them may have known the old password.
// Following the link proves control of the inbox, so the address becomes
// verified. MFA stays on: the reset never skips the second factor.
func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput, c Client) error {
	if !core.ValidOpaqueToken(in.Token) {
		return ErrTokenInvalid
	}
	// Everything that can reject the password runs before the token is
	// spent, so a refused password does not burn the link.
	var v domain.Validation
	v.Check("password", domain.ValidatePassword(in.Password))
	if err := v.Err(); err != nil {
		return err
	}
	if s.breached(ctx, in.Password) {
		v.Add("password", domain.CodeBreached)
		return v.Err()
	}
	hash, err := s.Hasher.Hash(ctx, in.Password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := s.o.Now()
	t, err := s.AuthTokens.Consume(ctx, core.HashToken(in.Token), domain.PurposeResetPassword, now)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("consume reset token: %w", err)
	}
	u, err := s.Users.ByID(ctx, t.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if err := s.Users.ResetPassword(ctx, u.ID, hash, now); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("reset password: %w", err)
	}
	// A failure here surfaces as an error even though the password changed:
	// the owner then asks for another link, whose reset revokes again.
	if err := s.revokeAllSessions(ctx, u.ID, "", now); err != nil {
		return err
	}
	if err := s.AuthTokens.InvalidateFor(ctx, u.ID, domain.PurposeResetPassword, now); err != nil {
		s.o.Logger.WarnContext(ctx, "invalidate reset tokens", slog.String("error", err.Error()))
	}
	if err := s.Lockout.Reset(ctx, core.HashIdentifier("email", u.Email)); err != nil {
		s.o.Logger.WarnContext(ctx, "reset login lockout", slog.String("error", err.Error()))
	}
	s.record(ctx, audit.EventPasswordReset, u.ID, "", c, "")
	s.Metrics.PasswordReset()
	s.notify(ctx, u, email.TemplatePasswordChanged)
	return nil
}

// revokeAllSessions ends the sessions of userID but except, in the Redis
// denylist (no more access tokens) and in MongoDB (no more refreshes).
//
// Redis goes first: when it fails, nothing is revoked in MongoDB yet, so a
// retry finds the same sessions and denies their access tokens. Sessions
// opened between the two steps are denied after.
func (s *Service) revokeAllSessions(ctx context.Context, userID, except string, now time.Time) error {
	ttl := s.Tokens.TTL() + core.ClockLeeway
	ids, err := s.Sessions.ActiveIDs(ctx, userID, except, now)
	if err != nil {
		return fmt.Errorf("find sessions: %w", err)
	}
	if err := s.Revocations.RevokeSessions(ctx, ids, ttl); err != nil {
		return unavailable(err)
	}
	revoked, err := s.Sessions.RevokeAllForUser(ctx, userID, except, now)
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if late := missing(revoked, ids); len(late) > 0 {
		if err := s.Revocations.RevokeSessions(ctx, late, ttl); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

// missing returns the ids of got that are not in known.
func missing(got, known []string) []string {
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}
	var out []string
	for _, id := range got {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}

// notify sends a security notice to the owner, with a reset link in case
// they did not make the change.
func (s *Service) notify(ctx context.Context, u domain.User, tpl email.Template) {
	s.Mailer.Send(ctx, email.Message{To: u.Email, Locale: u.Preferences.Locale, Template: tpl, Link: s.o.AppURL.Join("/forgot-password")})
}
