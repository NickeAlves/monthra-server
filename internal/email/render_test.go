package email

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer()
	require.NoError(t, err)
	return r
}

func TestCatalogIsComplete(t *testing.T) {
	all := []Template{
		TemplateVerifyEmail, TemplateRegisterAttempt, TemplateResetPassword,
		TemplatePasswordChanged, TemplateMFAEnabled, TemplateMFADisabled,
		TemplateAccountDeleted,
	}
	noAction := map[Template]bool{TemplateAccountDeleted: true}
	for _, tpl := range all {
		for _, l := range i18n.Supported {
			c, ok := catalog[tpl][l]
			require.True(t, ok, "%s misses %s", tpl, l)
			assert.NotEmpty(t, c.Subject, "%s %s subject", tpl, l)
			assert.NotEmpty(t, c.Heading, "%s %s heading", tpl, l)
			assert.NotEmpty(t, c.Lines, "%s %s lines", tpl, l)
			if noAction[tpl] {
				assert.Empty(t, c.Action, "%s %s has no button", tpl, l)
			} else {
				assert.NotEmpty(t, c.Action, "%s %s action", tpl, l)
			}
			assert.NotEmpty(t, c.Footer, "%s %s footer", tpl, l)
		}
	}
	assert.Len(t, catalog, len(all), "add new templates to this test")
}

func TestRenderEveryTemplateAndLocale(t *testing.T) {
	r := newRenderer(t)
	link := "https://app.monthra.example/verify-email#token=abc_DEF-123"
	for tpl := range catalog {
		for _, l := range i18n.Supported {
			e, err := r.Render(Message{To: "ana@example.com", Locale: l, Template: tpl, Link: link, LinkTTL: 30 * time.Minute})
			require.NoError(t, err)
			c := catalog[tpl][l]
			assert.Equal(t, tpl, e.Template)
			assert.Equal(t, "ana@example.com", e.To)
			assert.Equal(t, c.Subject, e.Subject)
			assert.Contains(t, e.HTML, `lang="`+string(l)+`"`)
			if c.Action == "" {
				assert.NotContains(t, e.HTML, "href=", "no button without an action")
				assert.NotContains(t, e.Text, link)
			} else {
				assert.Contains(t, e.HTML, `href="`+link+`"`)
				assert.Contains(t, e.Text, c.Action+": "+link)
			}
			assert.NotContains(t, e.Text+e.HTML, "{minutes}")
		}
	}
}

func TestRenderSubstitutesTheLinkLifetime(t *testing.T) {
	e, err := newRenderer(t).Render(Message{Locale: i18n.EnUS, Template: TemplateVerifyEmail, Link: "https://x.example/v", LinkTTL: 30 * time.Minute})
	require.NoError(t, err)
	assert.Contains(t, e.Text, "valid for 30 minutes")
	assert.Contains(t, e.HTML, "valid for 30 minutes")

	e, err = newRenderer(t).Render(Message{Locale: i18n.PtBR, Template: TemplateResetPassword, Link: "https://x.example/r", LinkTTL: 15 * time.Minute})
	require.NoError(t, err)
	assert.Contains(t, e.Text, "vale por 15 minutos")
}

func TestRenderEscapesTheLink(t *testing.T) {
	e, err := newRenderer(t).Render(Message{Locale: i18n.EnUS, Template: TemplateVerifyEmail, Link: `javascript:alert("x")`})
	require.NoError(t, err)
	assert.NotContains(t, e.HTML, "javascript:", "html/template neutralizes unsafe URL schemes")

	e, err = newRenderer(t).Render(Message{Locale: i18n.EnUS, Template: TemplateVerifyEmail, Link: `https://x.example/"><script>`})
	require.NoError(t, err)
	assert.NotContains(t, e.HTML, "<script>")
}

func TestRenderFallsBackToTheDefaultLocale(t *testing.T) {
	e, err := newRenderer(t).Render(Message{Locale: "fr-FR", Template: TemplateRegisterAttempt, Link: "https://x.example/f"})
	require.NoError(t, err)
	assert.Equal(t, catalog[TemplateRegisterAttempt][i18n.Default].Subject, e.Subject)
	assert.True(t, strings.Contains(e.HTML, `lang="pt-BR"`))
}

func TestRenderRejectsUnknownTemplates(t *testing.T) {
	_, err := newRenderer(t).Render(Message{Template: "nope"})
	assert.ErrorContains(t, err, "unknown email template")
}
