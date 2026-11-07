// Package middleware holds the HTTP middleware chain of the API.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"

	"github.com/NickeAlves/monthra-server/internal/observability"
)

// RequestIDHeader carries the request id in both directions.
const RequestIDHeader = "X-Request-ID"

// validRequestID bounds what we accept from clients/proxies, so a forged id
// cannot inject newlines or huge values into the logs.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID reuses a well-formed incoming X-Request-ID (set by the proxy) or
// generates one, stores it in the request metadata and echoes it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := observability.WithRequestMeta(r.Context(), &observability.RequestMeta{RequestID: id})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b)
}
