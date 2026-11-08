package dto

// The max tags only stop oversized input; the domain rules (email shape,
// password policy, name length) are checked by the use case.

// RegisterRequest is the body of POST /auth/register.
type RegisterRequest struct {
	Name         string `json:"name" validate:"required,max=400"`
	Email        string `json:"email" validate:"required,max=254"`
	Password     string `json:"password" validate:"required,max=4096"`
	CaptchaToken string `json:"captchaToken" validate:"max=2048"`
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Email        string `json:"email" validate:"required,max=254"`
	Password     string `json:"password" validate:"required,max=4096"`
	CaptchaToken string `json:"captchaToken" validate:"max=2048"`
}

// VerifyEmailRequest is the body of POST /auth/verify-email.
type VerifyEmailRequest struct {
	Token string `json:"token" validate:"required,max=128"`
}

// ResendVerificationRequest is the body of POST /auth/verify-email/resend.
type ResendVerificationRequest struct {
	Email string `json:"email" validate:"required,max=254"`
}

// ForgotPasswordRequest is the body of POST /auth/forgot-password.
type ForgotPasswordRequest struct {
	Email string `json:"email" validate:"required,max=254"`
}

// ResetPasswordRequest is the body of POST /auth/reset-password.
type ResetPasswordRequest struct {
	Token    string `json:"token" validate:"required,max=128"`
	Password string `json:"password" validate:"required,max=4096"`
}

// RefreshRequest is the body of POST /auth/refresh from native clients;
// browsers send the refresh cookie and no body.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken" validate:"required,max=128"`
}

// SessionResponse answers login and refresh. Browsers get only the
// lifetimes (the tokens travel in HttpOnly cookies); native clients get the
// tokens too.
type SessionResponse struct {
	TokenType        string `json:"tokenType,omitempty"`
	AccessToken      string `json:"accessToken,omitempty"`
	RefreshToken     string `json:"refreshToken,omitempty"`
	ExpiresIn        int    `json:"expiresIn"`
	RefreshExpiresIn int    `json:"refreshExpiresIn"`
}

// MFAChallengeResponse answers a correct password on an account with MFA on:
// the client sends MFAToken back to POST /auth/mfa/verify with a code.
type MFAChallengeResponse struct {
	MFARequired bool   `json:"mfaRequired"`
	MFAToken    string `json:"mfaToken"`
	ExpiresIn   int    `json:"expiresIn"`
}

// MFAVerifyRequest is the body of POST /auth/mfa/verify. Code is a TOTP code
// or a recovery code.
type MFAVerifyRequest struct {
	MFAToken string `json:"mfaToken" validate:"required,max=128"`
	Code     string `json:"code" validate:"required,max=64"`
}

// MFASetupRequest is the body of POST /auth/mfa/setup.
type MFASetupRequest struct {
	Password string `json:"password" validate:"required,max=4096"`
}

// MFASetupResponse carries the new secret, for typing by hand, and the
// otpauth:// URI, for a QR code.
type MFASetupResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauthUri"`
	ExpiresIn  int    `json:"expiresIn"`
}

// MFAEnableRequest is the body of POST /auth/mfa/enable.
type MFAEnableRequest struct {
	Code string `json:"code" validate:"required,max=64"`
}

// MFAEnableResponse carries the recovery codes, shown once.
type MFAEnableResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

// MFADisableRequest is the body of POST /auth/mfa/disable.
type MFADisableRequest struct {
	Password string `json:"password" validate:"required,max=4096"`
	Code     string `json:"code" validate:"required,max=64"`
}
