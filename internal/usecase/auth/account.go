package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
)

// MaxListedSessions caps the session list.
const MaxListedSessions = 100

// ChangePasswordInput is the current password (step-up) and the new one.
type ChangePasswordInput struct {
	CurrentPassword string
	NewPassword     string
}

// ChangePassword sets a new password for a signed-in caller. It asks for the
// current one, so a stolen session alone cannot take the account over, then
// signs out every other session and voids pending reset links. The caller's
// session stays.
func (s *Service) ChangePassword(ctx context.Context, p core.Principal, in ChangePasswordInput, c Client) error {
	var v domain.Validation
	v.Check("newPassword", domain.ValidatePassword(in.NewPassword))
	if err := v.Err(); err != nil {
		return err
	}
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return err
	}
	if err := s.reauthenticate(ctx, u, in.CurrentPassword, c); err != nil {
		return err
	}
	if s.breached(ctx, in.NewPassword) {
		v.Add("newPassword", domain.CodeBreached)
		return v.Err()
	}
	hash, err := s.Hasher.Hash(ctx, in.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	// The other sessions go first: when Redis is down nothing has changed,
	// and the owner retries with the same current password.
	now := s.o.Now()
	if err := s.revokeAllSessions(ctx, u.ID, p.SessionID, now); err != nil {
		return err
	}
	if err := s.Users.UpdatePasswordHash(ctx, u.ID, hash, now); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrUnauthorized
		}
		return fmt.Errorf("update password: %w", err)
	}
	if err := s.AuthTokens.InvalidateFor(ctx, u.ID, domain.PurposeResetPassword, now); err != nil {
		s.o.Logger.WarnContext(ctx, "invalidate reset tokens", slog.String("error", err.Error()))
	}
	s.record(ctx, audit.EventPasswordChanged, u.ID, p.SessionID, c, "")
	s.notify(ctx, u, email.TemplatePasswordChanged)
	return nil
}

// SessionInfo is one signed-in device of the caller.
type SessionInfo struct {
	domain.Session
	// Current marks the session of the request.
	Current bool
}

// ListSessions returns the caller's active sessions, the most recently used first.
func (s *Service) ListSessions(ctx context.Context, p core.Principal) ([]SessionInfo, error) {
	sessions, err := s.Sessions.ListActive(ctx, p.UserID, s.o.Now(), MaxListedSessions)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]SessionInfo, len(sessions))
	for i, sess := range sessions {
		out[i] = SessionInfo{Session: sess, Current: sess.ID == p.SessionID}
	}
	return out, nil
}

// RevokeSession signs out one of the caller's sessions and reports whether
// it was the caller's own. Any session that is not the caller's answers
// ErrSessionNotFound, the same as an unknown one (no IDOR, no probing).
func (s *Service) RevokeSession(ctx context.Context, p core.Principal, sessionID string, c Client) (current bool, err error) {
	now := s.o.Now()
	ok, err := s.Sessions.RevokeForUser(ctx, p.UserID, sessionID, now)
	if err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	if !ok {
		return false, ErrSessionNotFound
	}
	// After MongoDB, since ownership is checked there; a Redis failure is
	// safe to retry, as revoking an owned session again still matches.
	if err := s.Revocations.RevokeSession(ctx, sessionID, s.Tokens.TTL()+core.ClockLeeway); err != nil {
		return false, unavailable(err)
	}
	s.record(ctx, audit.EventSessionRevoked, p.UserID, sessionID, c, "")
	return sessionID == p.SessionID, nil
}

// RevokeOtherSessions signs out every session of the caller but the current one.
func (s *Service) RevokeOtherSessions(ctx context.Context, p core.Principal, c Client) error {
	if err := s.revokeAllSessions(ctx, p.UserID, p.SessionID, s.o.Now()); err != nil {
		return err
	}
	s.record(ctx, audit.EventSessionsRevoked, p.UserID, p.SessionID, c, "")
	return nil
}

// DeleteAccountInput is the password and, with MFA on, a current code.
type DeleteAccountInput struct {
	Password string
	Code     string
}

// DeleteAccount erases the caller's account and data (LGPD). It asks for the
// password again, and for a code when MFA is on, then:
//
//  1. denies every session in Redis and revokes it in MongoDB, so no access
//     or refresh token works from here on;
//  2. runs the Erasers in order (feature data, then the auth stores);
//  3. deletes the account last, so an interrupted deletion leaves an account
//     its owner can sign in to and delete again. Every step is idempotent.
//
// The audit log is kept until its TTL: security access records are retained
// as the law requires (Marco Civil art. 15, LGPD art. 16).
func (s *Service) DeleteAccount(ctx context.Context, p core.Principal, in DeleteAccountInput, c Client) error {
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return err
	}
	if err := s.reauthenticate(ctx, u, in.Password, c); err != nil {
		return err
	}
	emailKey := core.HashIdentifier("email", u.Email)
	factor := ""
	if u.MFAEnabled() {
		if in.Code == "" {
			return ErrMFACodeInvalid
		}
		var reason string
		factor, reason, err = s.checkSecondFactor(ctx, u, in.Code)
		if err != nil {
			return err
		}
		if factor == "" {
			s.secondFactorFailed(ctx, u.ID, emailKey, c, reason)
			return ErrMFACodeInvalid
		}
	}

	now := s.o.Now()
	if err := s.revokeAllSessions(ctx, u.ID, "", now); err != nil {
		return err
	}
	if err := s.Revocations.DenyToken(ctx, p.TokenID, p.ExpiresAt.Sub(now)+core.ClockLeeway); err != nil {
		return unavailable(err)
	}
	for _, e := range s.Erasers {
		if err := e.EraseUserData(ctx, u.ID); err != nil {
			return fmt.Errorf("erase user data: %w", err)
		}
	}
	if err := s.Users.Delete(ctx, u.ID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	// A new account on the same address starts with a clean slate.
	if err := s.Lockout.Reset(ctx, emailKey); err != nil {
		s.o.Logger.WarnContext(ctx, "reset login lockout", slog.String("error", err.Error()))
	}
	s.record(ctx, audit.EventAccountDeleted, u.ID, p.SessionID, c, factor)
	s.Mailer.Send(ctx, email.Message{To: u.Email, Locale: u.Preferences.Locale, Template: email.TemplateAccountDeleted})
	return nil
}
