package i18n

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromAcceptLanguage(t *testing.T) {
	tests := map[string]Locale{
		"":                        PtBR,
		"pt-BR":                   PtBR,
		"pt":                      PtBR,
		"es-ES":                   EsES,
		"es":                      EsES,
		"es-AR,es;q=0.9":          EsES,
		"en-US":                   EnUS,
		"en-GB,en;q=0.8":          EnUS,
		"fr-FR":                   PtBR,
		"fr-FR,en;q=0.5":          EnUS,
		"de;q=0.9,es;q=0.8":       EsES,
		"not a valid header;;;q=": PtBR,
	}
	for header, want := range tests {
		t.Run(header, func(t *testing.T) {
			assert.Equal(t, want, FromAcceptLanguage(header))
		})
	}
}

func TestParse(t *testing.T) {
	l, ok := Parse("es-ES")
	assert.True(t, ok)
	assert.Equal(t, EsES, l)

	_, ok = Parse("es")
	assert.False(t, ok, "only exact supported tags are accepted")
}

func TestContextLocale(t *testing.T) {
	assert.Equal(t, Default, FromContext(context.Background()))
	ctx := WithLocale(context.Background(), EnUS)
	assert.Equal(t, EnUS, FromContext(ctx))
}

func TestCatalogIsComplete(t *testing.T) {
	for _, key := range Keys() {
		for _, l := range Supported {
			m, ok := catalog[key][l]
			if assert.Truef(t, ok, "key %q has no %s translation", key, l) {
				assert.NotEmptyf(t, m.Title, "key %q has an empty %s title", key, l)
				assert.NotEmptyf(t, m.Detail, "key %q has an empty %s detail", key, l)
			}
		}
	}
}

func TestTFallbacks(t *testing.T) {
	assert.Equal(t, "Not found", T(EnUS, ErrNotFound).Title)
	assert.Equal(t, "Não encontrado", T(Locale("xx-XX"), ErrNotFound).Title, "unknown locale falls back to pt-BR")
	assert.Equal(t, "unknown_key", T(EnUS, Key("unknown_key")).Title, "unknown key falls back to the key")
}
