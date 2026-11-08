package email

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/config"
)

func TestNewSMTPAcceptsEveryTLSMode(t *testing.T) {
	for _, mode := range []config.SMTPTLS{config.SMTPTLSNone, config.SMTPTLSStartTLS, config.SMTPTLSImplicit} {
		_, err := NewSMTP(config.SMTP{Host: "smtp.example", Port: 587, From: "no-reply@monthra.example", TLS: mode, Timeout: time.Second})
		assert.NoError(t, err, mode)
	}
	_, err := NewSMTP(config.SMTP{Host: "smtp.example", Port: 587, From: "a@b.c", TLS: config.SMTPTLSStartTLS, Username: "relay", Password: "pw", Timeout: time.Second})
	assert.NoError(t, err)

	_, err = NewSMTP(config.SMTP{Host: "smtp.example", Port: 587, TLS: "ssl"})
	assert.ErrorContains(t, err, "unsupported SMTP_TLS")
	_, err = NewSMTP(config.SMTP{Host: "smtp.example", Port: 0, TLS: config.SMTPTLSNone})
	assert.ErrorContains(t, err, "smtp client")
}

func TestSMTPSendFailsFastWithoutAServer(t *testing.T) {
	s, err := NewSMTP(config.SMTP{Host: "127.0.0.1", Port: 1, From: "no-reply@monthra.example", TLS: config.SMTPTLSNone, Timeout: time.Second})
	require.NoError(t, err)
	err = s.Send(context.Background(), Email{To: "ana@example.com", Subject: "s", Text: "t", HTML: "<p>t</p>"})
	assert.ErrorContains(t, err, "smtp send")

	err = s.Send(context.Background(), Email{To: "not an address"})
	assert.ErrorContains(t, err, "to address")

	bad, err := NewSMTP(config.SMTP{Host: "127.0.0.1", Port: 1, From: "broken", TLS: config.SMTPTLSNone, Timeout: time.Second})
	require.NoError(t, err)
	assert.ErrorContains(t, bad.Send(context.Background(), Email{To: "ana@example.com"}), "from address")
}
