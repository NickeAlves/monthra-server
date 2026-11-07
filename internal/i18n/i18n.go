// Package i18n picks the response language (pt-BR, es-ES or en-US) and holds
// the translated API messages.
package i18n

import (
	"context"

	"golang.org/x/text/language"
)

// Locale is one of the supported response languages.
type Locale string

// Supported locales. PtBR is the default.
const (
	PtBR Locale = "pt-BR"
	EsES Locale = "es-ES"
	EnUS Locale = "en-US"

	Default = PtBR
)

// Supported lists the locales in matcher order (the first one is the fallback).
var Supported = []Locale{PtBR, EsES, EnUS}

var matcher = language.NewMatcher([]language.Tag{
	language.BrazilianPortuguese,
	language.EuropeanSpanish,
	language.AmericanEnglish,
})

// Parse returns the locale for an exact tag such as a stored user preference.
func Parse(tag string) (Locale, bool) {
	for _, l := range Supported {
		if string(l) == tag {
			return l, true
		}
	}
	return "", false
}

// FromAcceptLanguage picks the best supported locale for an Accept-Language
// header, falling back to pt-BR when nothing supported matches.
func FromAcceptLanguage(header string) Locale {
	if header == "" {
		return Default
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(tags) == 0 {
		return Default
	}
	_, index, confidence := matcher.Match(tags...)
	if confidence == language.No {
		return Default
	}
	return Supported[index]
}

type localeKey struct{}

// WithLocale stores the response locale in ctx.
func WithLocale(ctx context.Context, l Locale) context.Context {
	return context.WithValue(ctx, localeKey{}, l)
}

// FromContext returns the locale stored in ctx, or the default.
func FromContext(ctx context.Context) Locale {
	if l, ok := ctx.Value(localeKey{}).(Locale); ok {
		return l
	}
	return Default
}
