//go:build integration

package email_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

func TestDispatcherDeliversThroughMailpit(t *testing.T) {
	mp := testutil.StartMailpit(t)
	sender, err := email.NewSMTP(config.SMTP{
		Host: mp.SMTPHost, Port: mp.SMTPPort, From: "Monthra <no-reply@monthra.example>",
		TLS: config.SMTPTLSNone, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	renderer, err := email.NewRenderer()
	require.NoError(t, err)
	d := email.NewDispatcher(email.DispatcherOptions{
		Sender: sender, Renderer: renderer, Logger: slog.New(slog.DiscardHandler),
		Metrics: observability.NewMetrics("test", "local"),
	})

	link := "http://localhost:3000/verify-email#token=abc"
	d.Send(context.Background(), email.Message{To: "ana@example.com", Locale: i18n.PtBR, Template: email.TemplateVerifyEmail, Link: link, LinkTTL: 30 * time.Minute})
	require.NoError(t, d.Close(context.Background()))

	msg := mp.WaitForEmail(t, "ana@example.com", "Confirme seu e-mail")
	assert.Equal(t, "no-reply@monthra.example", msg.From.Address)
	assert.Equal(t, "Monthra", msg.From.Name)
	assert.Contains(t, msg.Text, link)
	assert.Contains(t, msg.HTML, `href="`+link+`"`)
	assert.Contains(t, msg.Text, "O link vale por 30 minutos")
}
