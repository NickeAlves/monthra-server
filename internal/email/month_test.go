package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func monthNameOf(t *testing.T, month string, l i18n.Locale) string {
	t.Helper()
	name, err := monthName(month, l)
	require.NoError(t, err)
	return name
}

func TestMonthName(t *testing.T) {
	assert.Equal(t, "novembro de 2026", monthNameOf(t, "2026-11", i18n.PtBR))
	assert.Equal(t, "março de 2027", monthNameOf(t, "2027-03", i18n.PtBR))
	assert.Equal(t, "noviembre de 2026", monthNameOf(t, "2026-11", i18n.EsES))
	assert.Equal(t, "enero de 2027", monthNameOf(t, "2027-01", i18n.EsES))
	assert.Equal(t, "November 2026", monthNameOf(t, "2026-11", i18n.EnUS))
	assert.Equal(t, "diciembre de 1999", monthNameOf(t, "1999-12", i18n.EsES))
	assert.Equal(t, "novembro de 2026", monthNameOf(t, "2026-11", "fr-FR"), "the default locale")
	assert.Empty(t, monthNameOf(t, "", i18n.EnUS))
	for _, l := range i18n.Supported {
		assert.Len(t, monthNames[l], 12, l)
	}

	for _, bad := range []string{"2026-13", "2026-1", "11/2026", "2026-11-01"} {
		_, err := monthName(bad, i18n.EnUS)
		assert.Error(t, err, bad)
	}
}

func TestRenderBudgetAlerts(t *testing.T) {
	r := newRenderer(t)
	link := "https://app.monthra.example/budgets?month=2026-11"
	e, err := r.Render(Message{To: "ana@example.com", Locale: i18n.PtBR, Template: TemplateBudgetExceeded, Link: link, Month: "2026-11"})
	require.NoError(t, err)
	assert.Equal(t, "Um orçamento de novembro de 2026 passou do limite", e.Subject)
	assert.Contains(t, e.Text, "Seus gastos de novembro de 2026 passaram do limite")
	assert.Contains(t, e.Text, "Ver orçamentos: "+link)
	assert.Contains(t, e.HTML, `href="https://app.monthra.example/budgets?month=2026-11"`)

	e, err = r.Render(Message{Locale: i18n.EnUS, Template: TemplateBudgetWarning, Link: link, Month: "2026-11"})
	require.NoError(t, err)
	assert.Equal(t, "A November 2026 budget is close to its limit", e.Subject)

	_, err = r.Render(Message{Locale: i18n.EnUS, Template: TemplateBudgetWarning, Link: link})
	assert.ErrorContains(t, err, "needs a month")
	_, err = r.Render(Message{Locale: i18n.EnUS, Template: TemplateBudgetWarning, Link: link, Month: "november"})
	assert.ErrorContains(t, err, "invalid email month")
}
