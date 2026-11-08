// Package email renders and delivers the transactional emails. Delivery is
// asynchronous: callers enqueue a Message and never wait for (or learn the
// outcome of) the SMTP exchange, so response times reveal nothing about
// whether an address is registered.
package email

import (
	"context"
	"time"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Template names a kind of email.
type Template string

// Templates.
const (
	// TemplateVerifyEmail carries the link that confirms a new account.
	TemplateVerifyEmail Template = "verify_email"
	// TemplateRegisterAttempt tells the owner of an existing account that
	// someone tried to register the address again.
	TemplateRegisterAttempt Template = "register_attempt"
	// TemplateResetPassword carries the link that sets a new password.
	TemplateResetPassword Template = "reset_password"
	// TemplatePasswordChanged tells the owner that the password changed.
	TemplatePasswordChanged Template = "password_changed"
	// TemplateMFAEnabled tells the owner that two-step verification was turned on.
	TemplateMFAEnabled Template = "mfa_enabled"
	// TemplateMFADisabled tells the owner that two-step verification was turned off.
	TemplateMFADisabled Template = "mfa_disabled"
	// TemplateAccountDeleted confirms that the account and its data were
	// deleted. It has no call to action: there is no account left to act on.
	TemplateAccountDeleted Template = "account_deleted"
)

// Message is an email to render and send. It never carries user-chosen text:
// a sign-up name or note would let anyone put words into an email sent to
// someone else's address.
type Message struct {
	To       string
	Locale   i18n.Locale
	Template Template
	// Link is the call to action, if the template has one.
	Link string
	// LinkTTL is how long Link works, when it expires.
	LinkTTL time.Duration
}

// Email is a rendered message, ready to send.
type Email struct {
	Template Template
	To       string
	Subject  string
	Text     string
	HTML     string
}

// Sender delivers one rendered email.
type Sender interface {
	Send(ctx context.Context, e Email) error
}
