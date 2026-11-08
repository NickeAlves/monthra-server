package email

import (
	"context"
	"fmt"

	"github.com/wneessen/go-mail"

	"github.com/NickeAlves/monthra-server/internal/config"
)

// SMTP sends through a generic SMTP relay. The provider of staging and
// production is picked together with the cloud (Terraform); locally it is Mailpit.
type SMTP struct {
	host string
	from string
	opts []mail.Option
}

// NewSMTP builds the sender from the validated configuration.
func NewSMTP(cfg config.SMTP) (*SMTP, error) {
	opts := []mail.Option{mail.WithPort(cfg.Port), mail.WithTimeout(cfg.Timeout)}
	switch cfg.TLS {
	case config.SMTPTLSNone:
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	case config.SMTPTLSStartTLS:
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	case config.SMTPTLSImplicit:
		opts = append(opts, mail.WithSSL())
	default:
		return nil, fmt.Errorf("unsupported SMTP_TLS %q", cfg.TLS)
	}
	if cfg.Username != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain),
			mail.WithUsername(cfg.Username), mail.WithPassword(cfg.Password.Reveal()))
	}
	// Fail now on a bad option instead of on the first email.
	if _, err := mail.NewClient(cfg.Host, opts...); err != nil {
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	return &SMTP{host: cfg.Host, from: cfg.From, opts: opts}, nil
}

// Send delivers e over a new connection.
func (s *SMTP) Send(ctx context.Context, e Email) error {
	m := mail.NewMsg(mail.WithNoDefaultUserAgent())
	if err := m.From(s.from); err != nil {
		return fmt.Errorf("from address: %w", err)
	}
	if err := m.To(e.To); err != nil {
		return fmt.Errorf("to address: %w", err)
	}
	m.Subject(e.Subject)
	m.SetMessageID()
	m.SetDate()
	m.SetGenHeader(mail.Header("Auto-Submitted"), "auto-generated")
	m.SetBodyString(mail.TypeTextPlain, e.Text)
	m.AddAlternativeString(mail.TypeTextHTML, e.HTML)

	client, err := mail.NewClient(s.host, s.opts...)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}
