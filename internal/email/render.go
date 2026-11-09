package email

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"strconv"
	"strings"
	texttemplate "text/template"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// Renderer turns a Message into an Email. Every template shares one layout;
// the copy comes from the catalog.
type Renderer struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

// NewRenderer parses the embedded layouts.
func NewRenderer() (*Renderer, error) {
	html, err := htmltemplate.ParseFS(templateFS, "templates/layout.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse html layout: %w", err)
	}
	text, err := texttemplate.ParseFS(templateFS, "templates/layout.txt.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse text layout: %w", err)
	}
	return &Renderer{html: html, text: text}, nil
}

type view struct {
	Lang    string
	Subject string
	Heading string
	Lines   []string
	Action  string
	Link    string
	Footer  string
}

// Render builds the email for m in its locale, falling back to the default locale.
func (r *Renderer) Render(m Message) (Email, error) {
	byLocale, ok := catalog[m.Template]
	if !ok {
		return Email{}, fmt.Errorf("unknown email template %q", m.Template)
	}
	locale := m.Locale
	c, ok := byLocale[locale]
	if !ok {
		locale = i18n.Default
		c = byLocale[locale]
	}
	month, err := monthName(m.Month, locale)
	if err != nil {
		return Email{}, err
	}
	if month == "" && strings.Contains(c.Subject, "{month}") {
		return Email{}, fmt.Errorf("email template %q needs a month", m.Template)
	}
	fill := strings.NewReplacer("{minutes}", strconv.Itoa(int(m.LinkTTL.Minutes())), "{month}", month).Replace
	lines := make([]string, len(c.Lines))
	for i, l := range c.Lines {
		lines[i] = fill(l)
	}
	v := view{
		Lang: string(locale), Subject: fill(c.Subject), Heading: fill(c.Heading), Lines: lines,
		Action: c.Action, Link: m.Link, Footer: fill(c.Footer),
	}

	var html, text bytes.Buffer
	if err := r.html.Execute(&html, v); err != nil {
		return Email{}, fmt.Errorf("render %s html: %w", m.Template, err)
	}
	if err := r.text.Execute(&text, v); err != nil {
		return Email{}, fmt.Errorf("render %s text: %w", m.Template, err)
	}
	return Email{Template: m.Template, To: m.To, Subject: v.Subject, Text: text.String(), HTML: html.String()}, nil
}
