package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

type memUsers struct {
	users     map[string]domain.User
	err       error
	localeErr error
	writes    int
}

func newUsers() *memUsers {
	u := domain.NewUser("Ana", "ana@example.com", "hash", i18n.EsES, t0)
	u.ID = "u1"
	return &memUsers{users: map[string]domain.User{"u1": u}}
}

func (m *memUsers) ByID(_ context.Context, id string) (domain.User, error) {
	if m.err != nil {
		return domain.User{}, m.err
	}
	u, ok := m.users[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (m *memUsers) Locale(_ context.Context, id string) (i18n.Locale, error) {
	if m.localeErr != nil {
		return "", m.localeErr
	}
	u, ok := m.users[id]
	if !ok {
		return "", domain.ErrNotFound
	}
	return u.Preferences.Locale, nil
}

func (m *memUsers) UpdateName(_ context.Context, id, name string, at time.Time) error {
	return m.update(id, at, func(u *domain.User) { u.Name = name })
}

func (m *memUsers) UpdatePreferences(_ context.Context, id string, p domain.Preferences, at time.Time) error {
	return m.update(id, at, func(u *domain.User) { u.Preferences = p })
}

func (m *memUsers) update(id string, at time.Time, f func(*domain.User)) error {
	if m.err != nil {
		return m.err
	}
	u, ok := m.users[id]
	if !ok {
		return domain.ErrNotFound
	}
	f(&u)
	u.UpdatedAt = at
	m.users[id] = u
	m.writes++
	return nil
}

type memLocales struct {
	values map[string]i18n.Locale
	err    error
	setErr error
}

func newLocales() *memLocales { return &memLocales{values: map[string]i18n.Locale{}} }

func (m *memLocales) Get(_ context.Context, id string) (i18n.Locale, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	l, ok := m.values[id]
	return l, ok, nil
}

func (m *memLocales) SetIfAbsent(_ context.Context, id string, l i18n.Locale) error {
	if m.setErr != nil {
		return m.setErr
	}
	if _, ok := m.values[id]; !ok {
		m.values[id] = l
	}
	return nil
}

func (m *memLocales) Set(_ context.Context, id string, l i18n.Locale) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.values[id] = l
	return nil
}

func newService(users *memUsers, locales *memLocales) *Service {
	return NewService(users, locales, Options{Now: func() time.Time { return t0.Add(time.Hour) }})
}

func ptr[T any](v T) *T { return &v }

func TestMe(t *testing.T) {
	users := newUsers()
	s := newService(users, newLocales())
	u, err := s.Me(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, "Ana", u.Name)

	_, err = s.Me(context.Background(), "gone")
	assert.ErrorIs(t, err, ErrAccountNotFound)

	users.err = errors.New("mongo down")
	_, err = s.Me(context.Background(), "u1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAccountNotFound)
}

func TestUpdateName(t *testing.T) {
	ctx := context.Background()
	users := newUsers()
	s := newService(users, newLocales())

	u, err := s.UpdateName(ctx, "u1", "  Ana Maria  ")
	require.NoError(t, err)
	assert.Equal(t, "Ana Maria", u.Name, "trimmed")
	assert.Equal(t, t0.Add(time.Hour), u.UpdatedAt)

	_, err = s.UpdateName(ctx, "u1", "   ")
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []domain.FieldError{{Field: "name", Code: domain.CodeRequired}}, ve.Fields)
	assert.Equal(t, 1, users.writes, "an invalid name is not stored")

	_, err = s.UpdateName(ctx, "gone", "Bia")
	assert.ErrorIs(t, err, ErrAccountNotFound)

	users.err = errors.New("mongo down")
	_, err = s.UpdateName(ctx, "u1", "Bia")
	require.Error(t, err)
}

func TestUpdatePreferences(t *testing.T) {
	ctx := context.Background()
	users, locales := newUsers(), newLocales()
	locales.values["u1"] = i18n.EsES
	s := newService(users, locales)

	got, err := s.Preferences(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, domain.DefaultPreferences(i18n.EsES), got)

	got, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{})
	require.NoError(t, err)
	assert.Equal(t, domain.DefaultPreferences(i18n.EsES), got)
	assert.Zero(t, users.writes, "an empty patch stores nothing")

	got, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{Theme: ptr("dark")})
	require.NoError(t, err)
	assert.Equal(t, domain.ThemeDark, got.Theme)
	assert.Equal(t, i18n.EsES, locales.values["u1"])

	got, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{Locale: ptr("en-US"), Currency: ptr("USD")})
	require.NoError(t, err)
	assert.Equal(t, domain.Preferences{Locale: i18n.EnUS, Theme: domain.ThemeDark, Currency: domain.CurrencyUSD}, got)
	assert.Equal(t, got, users.users["u1"].Preferences)
	assert.Equal(t, i18n.EnUS, locales.values["u1"], "a new locale is written through")

	_, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{Locale: ptr("pt-BR"), Theme: ptr("sepia")})
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []domain.FieldError{{Field: "theme", Code: domain.CodeInvalid}}, ve.Fields)
	assert.Equal(t, i18n.EnUS, users.users["u1"].Preferences.Locale, "nothing is stored")

	_, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{Theme: ptr("dark")})
	require.NoError(t, err)
	assert.Equal(t, 2, users.writes, "an unchanged value stores nothing")

	locales.setErr = errors.New("redis down")
	got, err = s.UpdatePreferences(ctx, "u1", domain.PreferencesPatch{Locale: ptr("pt-BR")})
	require.NoError(t, err, "a cache failure does not fail the change")
	assert.Equal(t, i18n.PtBR, got.Locale)

	_, err = s.UpdatePreferences(ctx, "gone", domain.PreferencesPatch{Theme: ptr("light")})
	assert.ErrorIs(t, err, ErrAccountNotFound)
}

func TestUpdatePreferencesStoreFailure(t *testing.T) {
	users := &failingUpdates{memUsers: newUsers(), err: domain.ErrNotFound}
	s := NewService(users, newLocales(), Options{})
	_, err := s.UpdatePreferences(context.Background(), "u1", domain.PreferencesPatch{Theme: ptr("light")})
	assert.ErrorIs(t, err, ErrAccountNotFound, "deleted between the read and the write")

	users.err = errors.New("mongo down")
	_, err = s.UpdatePreferences(context.Background(), "u1", domain.PreferencesPatch{Theme: ptr("light")})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAccountNotFound)
}

type failingUpdates struct {
	*memUsers
	err error
}

func (f *failingUpdates) UpdatePreferences(context.Context, string, domain.Preferences, time.Time) error {
	return f.err
}

func TestLocale(t *testing.T) {
	ctx := context.Background()
	users, locales := newUsers(), newLocales()
	s := newService(users, locales)

	l, ok := s.Locale(ctx, "u1")
	assert.True(t, ok)
	assert.Equal(t, i18n.EsES, l, "a miss reads the account")
	assert.Equal(t, i18n.EsES, locales.values["u1"], "and fills the cache")

	locales.values["u1"] = i18n.EnUS
	l, ok = s.Locale(ctx, "u1")
	assert.True(t, ok)
	assert.Equal(t, i18n.EnUS, l, "a hit skips the account")

	_, ok = s.Locale(ctx, "gone")
	assert.False(t, ok, "an unknown account keeps the request locale")

	locales.err = errors.New("redis down")
	l, ok = s.Locale(ctx, "u1")
	assert.True(t, ok, "the cache failing falls back to the account")
	assert.Equal(t, i18n.EsES, l)

	users.localeErr = errors.New("mongo down")
	_, ok = s.Locale(ctx, "u1")
	assert.False(t, ok, "fails open")

	users.localeErr = nil
	locales.err, locales.setErr = nil, errors.New("redis down")
	delete(locales.values, "u1")
	l, ok = s.Locale(ctx, "u1")
	assert.True(t, ok, "a failed cache fill still answers")
	assert.Equal(t, i18n.EsES, l)

	u := users.users["u1"]
	u.Preferences.Locale = "fr-FR"
	users.users["u1"] = u
	locales.setErr = nil
	_, ok = s.Locale(ctx, "u1")
	assert.False(t, ok, "an unsupported stored value is ignored")
}
