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
