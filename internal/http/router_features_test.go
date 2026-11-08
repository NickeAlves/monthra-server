package http

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// echoFeature answers GET /echo with the caller's user id and locale.
type echoFeature struct{}

func (echoFeature) Mount(r chi.Router) {
	r.Get("/echo", func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte(p.UserID + " " + string(i18n.FromContext(r.Context()))))
	})
}

func TestFeatureRoutesRunBehindAuthentication(t *testing.T) {
	h := newMeRouter(t, echoFeature{})

	w := do(h, http.MethodGet, "/api/v1/echo", "", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = do(h, http.MethodGet, "/api/v1/echo", "", map[string]string{"Authorization": "Bearer good"})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "u1 en-US", w.Body.String(), "features see the caller and the stored locale")
}
