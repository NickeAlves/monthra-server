package middleware

import (
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// BodyLimit rejects requests whose declared Content-Length exceeds max with 413,
// and caps the body of every other request so a chunked upload cannot exceed it
// either (reads past the limit fail with *http.MaxBytesError).
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				problem.Write(w, r, http.StatusRequestEntityTooLarge, i18n.ErrPayloadTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
