package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Recoverer turns a panic into a 500 problem response. The stack trace goes to
// the log only, never to the client.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the sanctioned way to abort a response; let net/http handle it.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				logger.ErrorContext(r.Context(), "panic while serving request",
					slog.String("panic", fmt.Sprint(rec)),
					slog.String("stack", string(debug.Stack())),
				)
				if r.Header.Get("Connection") != "Upgrade" {
					problem.Write(w, r, http.StatusInternalServerError, i18n.ErrInternal)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
