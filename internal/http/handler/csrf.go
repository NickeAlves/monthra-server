package handler

import (
	"encoding/json"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
)

// CSRFToken serves GET /api/v1/auth/csrf: it sets the readable CSRF cookie
// and returns the same token in the body, for web apps on another origin of
// the same site, which cannot read the API's cookies.
//
// A request that already carries a valid token gets that token back, so
// several open tabs keep working with the token each one holds.
type CSRFToken struct {
	Signer  csrf.Signer
	Cookies cookie.Policy
}

type csrfResponse struct {
	CSRFToken string `json:"csrfToken"`
}

func (h CSRFToken) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := h.Cookies.Get(r, cookie.CSRF, cookie.RootPath)
	if !ok || !h.Signer.Valid(token) {
		token = h.Signer.Issue()
		// Readable by the page (not HttpOnly): the double-submit pattern needs it.
		http.SetCookie(w, h.Cookies.New(cookie.CSRF, token, cookie.RootPath, false, 0))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(csrfResponse{CSRFToken: token})
}
