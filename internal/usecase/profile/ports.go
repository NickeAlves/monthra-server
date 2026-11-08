// Package profile implements the account holder's own profile and
// preferences (§5.5 /me, §7.6): display name, language, theme and currency.
// Credentials (password, sessions, MFA, deletion) live in usecase/auth.
package profile

import (
	"context"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Users reads and updates accounts.
type Users interface {
	ByID(ctx context.Context, id string) (domain.User, error)
	Locale(ctx context.Context, id string) (i18n.Locale, error)
	UpdateName(ctx context.Context, id, name string, at time.Time) error
	UpdatePreferences(ctx context.Context, id string, p domain.Preferences, at time.Time) error
}

// LocaleCache keeps the stored locale of each user close at hand. Readers
// fill a miss with SetIfAbsent and writers overwrite with Set, so a stale
// read never replaces a newer value.
type LocaleCache interface {
	Get(ctx context.Context, userID string) (i18n.Locale, bool, error)
	SetIfAbsent(ctx context.Context, userID string, l i18n.Locale) error
	Set(ctx context.Context, userID string, l i18n.Locale) error
}
