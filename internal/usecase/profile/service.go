package profile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// ErrAccountNotFound means the caller's account no longer exists; the HTTP
// layer answers it like an invalid session.
var ErrAccountNotFound = errors.New("account not found")

// Service serves the caller's profile and preferences.
type Service struct {
	users   Users
	locales LocaleCache
	logger  *slog.Logger
	now     func() time.Time
}

// Options tunes the Service.
type Options struct {
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// NewService returns the service.
func NewService(users Users, locales LocaleCache, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{users: users, locales: locales, logger: o.Logger, now: o.Now}
}

// Me returns the caller's account.
func (s *Service) Me(ctx context.Context, userID string) (domain.User, error) {
	u, err := s.users.ByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.User{}, ErrAccountNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("find user: %w", err)
	}
	return u, nil
}

// UpdateName changes the display name and returns the updated account.
func (s *Service) UpdateName(ctx context.Context, userID, rawName string) (domain.User, error) {
	name, err := domain.NormalizeName(rawName)
	if err != nil {
		var v domain.Validation
		v.Check("name", err)
		return domain.User{}, v.Err()
	}
	if err := s.users.UpdateName(ctx, userID, name, s.now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.User{}, ErrAccountNotFound
		}
		return domain.User{}, fmt.Errorf("update name: %w", err)
	}
	return s.Me(ctx, userID)
}

// Preferences returns the caller's preferences.
func (s *Service) Preferences(ctx context.Context, userID string) (domain.Preferences, error) {
	u, err := s.Me(ctx, userID)
	if err != nil {
		return domain.Preferences{}, err
	}
	return u.Preferences, nil
}

// UpdatePreferences applies a patch and returns the resulting preferences.
// Every field is checked before anything is stored: one unsupported value
// rejects the whole patch. A new locale is written through to the cache, so
// the next answers already use it.
func (s *Service) UpdatePreferences(ctx context.Context, userID string, patch domain.PreferencesPatch) (domain.Preferences, error) {
	current, err := s.Preferences(ctx, userID)
	if err != nil {
		return domain.Preferences{}, err
	}
	next, err := patch.Apply(current)
	if err != nil {
		return domain.Preferences{}, err
	}
	if next == current {
		return current, nil
	}
	if err := s.users.UpdatePreferences(ctx, userID, next, s.now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Preferences{}, ErrAccountNotFound
		}
		return domain.Preferences{}, fmt.Errorf("update preferences: %w", err)
	}
	if next.Locale != current.Locale {
		if err := s.locales.Set(ctx, userID, next.Locale); err != nil {
			// The cached value expires on its own; until then answers keep the old language.
			s.logger.WarnContext(ctx, "cache new locale", slog.String("error", err.Error()))
		}
	}
	return next, nil
}

// Locale returns the stored locale of userID for translating answers, from
// the cache or, on a miss, from the account. It fails open: when neither
// store answers, it reports false and the request keeps the Accept-Language
// locale, since the language guards nothing.
func (s *Service) Locale(ctx context.Context, userID string) (i18n.Locale, bool) {
	l, ok, err := s.locales.Get(ctx, userID)
	if err == nil && ok {
		return l, true
	}
	cacheUp := err == nil
	if err != nil {
		s.logger.WarnContext(ctx, "read cached locale", slog.String("error", err.Error()))
	}
	stored, err := s.users.Locale(ctx, userID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.logger.WarnContext(ctx, "read stored locale", slog.String("error", err.Error()))
		}
		return "", false
	}
	l, ok = i18n.Parse(string(stored))
	if !ok {
		return "", false
	}
	if cacheUp {
		if err := s.locales.SetIfAbsent(ctx, userID, l); err != nil {
			s.logger.WarnContext(ctx, "cache locale", slog.String("error", err.Error()))
		}
	}
	return l, true
}
