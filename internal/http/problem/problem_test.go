package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

func TestWriteTranslatesAndSetsHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/nope?x=1", nil)
	ctx := i18n.WithLocale(r.Context(), i18n.EsES)
	ctx = observability.WithRequestMeta(ctx, &observability.RequestMeta{RequestID: "req-9"})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	Write(w, r, http.StatusNotFound, i18n.ErrNotFound)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, ContentType, w.Header().Get("Content-Type"))
	assert.Equal(t, "es-ES", w.Header().Get("Content-Language"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var p Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	assert.Equal(t, Problem{
		Type:      "urn:monthra:problem:not_found",
		Title:     "No encontrado",
		Status:    http.StatusNotFound,
		Detail:    "El recurso solicitado no existe.",
		Instance:  "/api/v1/nope",
		Code:      "not_found",
		RequestID: "req-9",
	}, p)
}

func TestWriteDefaultsToPortuguese(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()

	Write(w, r, http.StatusInternalServerError, i18n.ErrInternal)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "Erro interno", body["title"])
	assert.NotContains(t, body, "requestId", "empty request id is omitted")
	assert.NotContains(t, body, "errors")
}
