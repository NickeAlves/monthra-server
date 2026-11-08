package dto

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// MeResponse is the caller's account, as GET and PATCH /me return it.
type MeResponse struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Email         string      `json:"email"`
	EmailVerified bool        `json:"emailVerified"`
	MFA           MFAStatus   `json:"mfa"`
	Preferences   Preferences `json:"preferences"`
	CreatedAt     time.Time   `json:"createdAt"`
}

// MFAStatus tells whether two-step verification is on and how many unused
// recovery codes are left.
type MFAStatus struct {
	Enabled                bool `json:"enabled"`
	RecoveryCodesRemaining int  `json:"recoveryCodesRemaining"`
}

// Preferences are the profile settings.
type Preferences struct {
	Locale   string `json:"locale"`
	Theme    string `json:"theme"`
	Currency string `json:"currency"`
}

// NewMeResponse maps an account to its public view: no hash, secret or code.
func NewMeResponse(u domain.User) MeResponse {
	resp := MeResponse{
		ID: u.ID, Name: u.Name, Email: u.Email, EmailVerified: u.EmailVerified(),
		Preferences: NewPreferences(u.Preferences), CreatedAt: u.CreatedAt.UTC(),
	}
	if u.MFAEnabled() {
		resp.MFA = MFAStatus{Enabled: true, RecoveryCodesRemaining: len(u.MFA.RecoveryCodes)}
	}
	return resp
}

// NewPreferences maps preferences to their JSON form.
func NewPreferences(p domain.Preferences) Preferences {
	return Preferences{Locale: string(p.Locale), Theme: string(p.Theme), Currency: p.Currency}
}

// UpdateMeRequest is the body of PATCH /me. Only these fields can change.
type UpdateMeRequest struct {
	Name *string `json:"name" validate:"required,max=400"`
}

// UpdatePreferencesRequest is the body of PATCH /me/preferences. Absent (or
// null) fields stay as they are.
type UpdatePreferencesRequest struct {
	Locale   *string `json:"locale" validate:"omitempty,max=16"`
	Theme    *string `json:"theme" validate:"omitempty,max=16"`
	Currency *string `json:"currency" validate:"omitempty,max=8"`
}

// Patch returns the domain patch of the request.
func (r UpdatePreferencesRequest) Patch() domain.PreferencesPatch {
	return domain.PreferencesPatch{Locale: r.Locale, Theme: r.Theme, Currency: r.Currency}
}

// ChangePasswordRequest is the body of POST /me/password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" validate:"required,max=4096"`
	NewPassword     string `json:"newPassword" validate:"required,max=4096"`
}

// DeleteAccountRequest is the body of DELETE /me. Code is required when MFA is on.
type DeleteAccountRequest struct {
	Password string `json:"password" validate:"required,max=4096"`
	Code     string `json:"code" validate:"max=64"`
}

// SessionView is one signed-in device. LastUsedAt moves on each refresh, so
// it is accurate to the access token lifetime.
type SessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	UserAgent  string    `json:"userAgent"`
	IP         string    `json:"ip"`
	Current    bool      `json:"current"`
}

// SessionsResponse answers GET /me/sessions.
type SessionsResponse struct {
	Sessions []SessionView `json:"sessions"`
}
