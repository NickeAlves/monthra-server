package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/crypto/aead"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
)

// MFASetupTTL is how long a setup waits for its first code.
const MFASetupTTL = 15 * time.Minute

// mfaSecretField is the additional data of sealed TOTP secrets, with the user id.
const mfaSecretField = "users.mfa_secret"

// MFA event labels of auth_mfa_total.
const (
	mfaEventEnabled      = "enabled"
	mfaEventDisabled     = "disabled"
	mfaEventRecoveryCode = "recovery_code_used"
)

// How a second factor was checked, stored as the reason of audit entries.
const (
	factorTOTP         = "totp"
	factorRecoveryCode = "recovery_code"
)

// Why a second factor was refused.
const (
	reasonWrongCode    = "wrong_code"
	reasonReplayedCode = "replayed_code"
)

// MFASetup is what an authenticator app needs to add the account: the
// base32 secret (typed by hand) or the otpauth:// URI (shown as a QR code).
type MFASetup struct {
	Secret    string
	URI       string
	ExpiresAt time.Time
}

// SetupMFA starts turning MFA on: it asks for the password again, so a
// stolen session alone cannot lock the owner out behind a second factor,
// and stores a new sealed secret that EnableMFA confirms. A new setup
// replaces an unfinished one.
func (s *Service) SetupMFA(ctx context.Context, p core.Principal, password string, c Client) (MFASetup, error) {
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return MFASetup{}, err
	}
	if u.MFAEnabled() {
		return MFASetup{}, ErrMFAAlreadyEnabled
	}
	if err := s.reauthenticate(ctx, u, password, c); err != nil {
		return MFASetup{}, err
	}
	key, err := core.NewTOTPKey(s.o.MFAIssuer, u.Email)
	if err != nil {
		return MFASetup{}, err
	}
	sealed, err := s.Sealer.Seal([]byte(key.Secret), mfaAAD(u.ID))
	if err != nil {
		return MFASetup{}, fmt.Errorf("seal mfa secret: %w", err)
	}
	now := s.o.Now()
	expiresAt := now.Add(MFASetupTTL)
	ok, err := s.Users.SetPendingMFA(ctx, u.ID, sealed, expiresAt, now)
	if err != nil {
		return MFASetup{}, fmt.Errorf("store mfa setup: %w", err)
	}
	if !ok {
		return MFASetup{}, ErrMFAAlreadyEnabled
	}
	s.record(ctx, audit.EventMFASetup, u.ID, p.SessionID, c, "")
	return MFASetup{Secret: key.Secret, URI: key.URI, ExpiresAt: expiresAt}, nil
}

// EnableMFA confirms a setup with a first code and turns MFA on. It returns
// the recovery codes, which are shown once and stored only as hashes.
func (s *Service) EnableMFA(ctx context.Context, p core.Principal, code string, c Client) ([]string, error) {
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return nil, err
	}
	if u.MFAEnabled() {
		return nil, ErrMFAAlreadyEnabled
	}
	now := s.o.Now()
	if !u.MFA.PendingActive(now) {
		return nil, ErrMFASetupRequired
	}
	secret, _, err := s.openSecret(u.ID, u.MFA.PendingSecret)
	if err != nil {
		return nil, err
	}
	step, ok, err := core.MatchTOTP(secret, code, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrMFACodeInvalid
	}
	codes := core.NewRecoveryCodes(core.RecoveryCodeCount)
	hashes := make([]string, len(codes))
	for i, rc := range codes {
		canonical, _ := core.NormalizeRecoveryCode(rc)
		hashes[i] = core.HashRecoveryCode(canonical)
	}
	enabled, err := s.Users.EnableMFA(ctx, u.ID, u.MFA.PendingSecret, hashes, step, now)
	if err != nil {
		return nil, fmt.Errorf("enable mfa: %w", err)
	}
	if !enabled {
		return nil, ErrMFASetupRequired // replaced or finished by a concurrent request
	}
	s.record(ctx, audit.EventMFAEnabled, u.ID, p.SessionID, c, "")
	s.Metrics.MFA(mfaEventEnabled)
	s.notify(ctx, u, email.TemplateMFAEnabled)
	return codes, nil
}

// DisableMFAInput is the password and a current code (TOTP or recovery).
type DisableMFAInput struct {
	Password string
	Code     string
}

// DisableMFA turns MFA off. It asks for both factors, so neither a stolen
// session nor a known password alone can remove the second one.
func (s *Service) DisableMFA(ctx context.Context, p core.Principal, in DisableMFAInput, c Client) error {
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return err
	}
	if !u.MFAEnabled() {
		return ErrMFANotEnabled
	}
	if err := s.reauthenticate(ctx, u, in.Password, c); err != nil {
		return err
	}
	emailKey := core.HashIdentifier("email", u.Email)
	factor, reason, err := s.checkSecondFactor(ctx, u, in.Code)
	if err != nil {
		return err
	}
	if factor == "" {
		s.secondFactorFailed(ctx, u.ID, emailKey, c, reason)
		return ErrMFACodeInvalid
	}
	ok, err := s.Users.DisableMFA(ctx, u.ID, s.o.Now())
	if err != nil {
		return fmt.Errorf("disable mfa: %w", err)
	}
	if !ok {
		return ErrMFANotEnabled
	}
	s.record(ctx, audit.EventMFADisabled, u.ID, p.SessionID, c, factor)
	s.Metrics.MFA(mfaEventDisabled)
	s.notify(ctx, u, email.TemplateMFADisabled)
	return nil
}

// VerifyMFAInput is the second step of a login.
type VerifyMFAInput struct {
	Token string
	Code  string
}

// VerifyMFA finishes a login with a TOTP or recovery code. A challenge takes
// core.MFAMaxAttempts codes, every wrong one counts toward the lockout of
// the address, and each code works only once.
func (s *Service) VerifyMFA(ctx context.Context, in VerifyMFAInput, c Client) (Tokens, error) {
	if !core.ValidOpaqueToken(in.Token) {
		return Tokens{}, ErrMFAChallengeInvalid
	}
	hash := core.HashToken(in.Token)
	ch, ok, err := s.MFAChallenges.Attempt(ctx, hash)
	if err != nil {
		return Tokens{}, unavailable(err)
	}
	if !ok {
		return Tokens{}, ErrMFAChallengeInvalid
	}
	if ch.Attempts > core.MFAMaxAttempts {
		s.dropChallenge(ctx, hash)
		return Tokens{}, ErrMFAChallengeInvalid
	}
	state, err := s.Lockout.Check(ctx, ch.EmailKey, c.ipSubject())
	if err != nil {
		return Tokens{}, unavailable(err)
	}
	if state.RetryAfter > 0 {
		s.Metrics.Login(resultLocked)
		return Tokens{}, &RateLimitedError{RetryAfter: state.RetryAfter}
	}
	u, err := s.Users.ByID(ctx, ch.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		s.dropChallenge(ctx, hash)
		return Tokens{}, ErrMFAChallengeInvalid
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("find user: %w", err)
	}
	if !u.MFAEnabled() {
		s.dropChallenge(ctx, hash) // turned off meanwhile: sign in again
		return Tokens{}, ErrMFAChallengeInvalid
	}

	factor, reason, err := s.checkSecondFactor(ctx, u, in.Code)
	if err != nil {
		return Tokens{}, err
	}
	if factor == "" {
		s.secondFactorFailed(ctx, u.ID, ch.EmailKey, c, reason)
		s.Metrics.Login(resultMFAFailure)
		if ch.Attempts >= core.MFAMaxAttempts {
			s.dropChallenge(ctx, hash)
		}
		return Tokens{}, ErrMFACodeInvalid
	}
	// The code is spent; now the challenge. Of two concurrent verifications
	// with valid codes, only the one that deletes it opens a session.
	won, err := s.MFAChallenges.Consume(ctx, hash)
	if err != nil {
		return Tokens{}, unavailable(err)
	}
	if !won {
		return Tokens{}, ErrMFAChallengeInvalid
	}
	tokens, err := s.finishLogin(ctx, u.ID, ch.EmailKey, c, factor)
	if err != nil {
		return Tokens{}, err
	}
	if factor == factorRecoveryCode {
		s.record(ctx, audit.EventMFARecoveryCodeUsed, u.ID, "", c, "")
		s.Metrics.MFA(mfaEventRecoveryCode)
	}
	return tokens, nil
}

func (s *Service) startChallenge(ctx context.Context, userID, emailKey string) (Challenge, error) {
	token := core.NewOpaqueToken()
	err := s.MFAChallenges.Create(ctx, core.HashToken(token), core.MFAChallenge{UserID: userID, EmailKey: emailKey}, core.MFAChallengeTTL)
	if err != nil {
		return Challenge{}, unavailable(err)
	}
	return Challenge{Token: token, ExpiresAt: s.o.Now().Add(core.MFAChallengeTTL)}, nil
}

func (s *Service) dropChallenge(ctx context.Context, hash string) {
	if _, err := s.MFAChallenges.Consume(ctx, hash); err != nil {
		s.o.Logger.WarnContext(ctx, "drop mfa challenge", slog.String("error", err.Error()))
	}
}

// checkSecondFactor checks a TOTP code (six digits) or a recovery code and
// spends it. It returns how it was checked, or "" and why it was refused.
func (s *Service) checkSecondFactor(ctx context.Context, u domain.User, code string) (factor, reason string, err error) {
	if core.LooksLikeTOTPCode(code) {
		secret, rotate, err := s.openSecret(u.ID, u.MFA.Secret)
		if err != nil {
			return "", "", err
		}
		step, ok, err := core.MatchTOTP(secret, code, s.o.Now())
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", reasonWrongCode, nil
		}
		fresh, err := s.Users.UseTOTPStep(ctx, u.ID, step)
		if err != nil {
			return "", "", fmt.Errorf("use totp step: %w", err)
		}
		if !fresh {
			return "", reasonReplayedCode, nil
		}
		if rotate {
			s.resealSecret(ctx, u, secret)
		}
		return factorTOTP, "", nil
	}
	canonical, ok := core.NormalizeRecoveryCode(code)
	if !ok {
		return "", reasonWrongCode, nil
	}
	used, err := s.Users.UseRecoveryCode(ctx, u.ID, core.HashRecoveryCode(canonical))
	if err != nil {
		return "", "", fmt.Errorf("use recovery code: %w", err)
	}
	if !used {
		return "", reasonWrongCode, nil
	}
	return factorRecoveryCode, "", nil
}

func (s *Service) secondFactorFailed(ctx context.Context, userID, emailKey string, c Client, reason string) {
	if _, err := s.Lockout.RecordFailure(ctx, emailKey, c.ipSubject()); err != nil {
		s.o.Logger.ErrorContext(ctx, "record mfa failure", slog.String("error", err.Error()))
	}
	s.record(ctx, audit.EventMFAFailure, userID, "", c, reason)
}

// reauthenticate asks for the password again before a sensitive change.
// Wrong passwords count toward the login lockout of the address, so a stolen
// session cannot be used to guess it.
func (s *Service) reauthenticate(ctx context.Context, u domain.User, password string, c Client) error {
	if password == "" || len(password) > maxPasswordBytes {
		return ErrPasswordIncorrect
	}
	emailKey := core.HashIdentifier("email", u.Email)
	state, err := s.Lockout.Check(ctx, emailKey, c.ipSubject())
	if err != nil {
		return unavailable(err)
	}
	if state.RetryAfter > 0 {
		return &RateLimitedError{RetryAfter: state.RetryAfter}
	}
	ok, _, err := s.Hasher.Verify(ctx, password, u.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		if _, err := s.Lockout.RecordFailure(ctx, emailKey, c.ipSubject()); err != nil {
			s.o.Logger.ErrorContext(ctx, "record reauthentication failure", slog.String("error", err.Error()))
		}
		s.record(ctx, audit.EventReauthFailure, u.ID, "", c, reasonWrongPassword)
		return ErrPasswordIncorrect
	}
	return nil
}

// currentUser loads the caller's account; a deleted account ends the session.
func (s *Service) currentUser(ctx context.Context, p core.Principal) (domain.User, error) {
	u, err := s.Users.ByID(ctx, p.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.User{}, ErrUnauthorized
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("find user: %w", err)
	}
	return u, nil
}

func mfaAAD(userID string) []byte { return aead.AAD(mfaSecretField, userID) }

func (s *Service) openSecret(userID, sealed string) (secret string, needsRotation bool, err error) {
	plain, rotate, err := s.Sealer.Open(sealed, mfaAAD(userID))
	if err != nil {
		return "", false, fmt.Errorf("open mfa secret: %w", err)
	}
	return string(plain), rotate, nil
}

// resealSecret seals a secret again under the active key, after a key
// rotation. A failure only delays it to the next sign-in.
func (s *Service) resealSecret(ctx context.Context, u domain.User, secret string) {
	sealed, err := s.Sealer.Seal([]byte(secret), mfaAAD(u.ID))
	if err == nil {
		_, err = s.Users.ReplaceMFASecret(ctx, u.ID, u.MFA.Secret, sealed)
	}
	if err != nil {
		s.o.Logger.WarnContext(ctx, "re-encrypt mfa secret", slog.String("error", err.Error()))
	}
}
