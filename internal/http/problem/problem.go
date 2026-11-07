// Package problem writes RFC 7807 / RFC 9457 problem details responses
// (application/problem+json) with translated titles.
package problem

import (
	"encoding/json"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// ContentType is the media type of problem details responses.
const ContentType = "application/problem+json"

// typePrefix namespaces problem types; the suffix is the stable code.
const typePrefix = "urn:monthra:problem:"

// Problem is a problem details object. Code is a stable, machine-readable
// identifier clients can switch on; Title and Detail are translated for humans.
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	Code      string       `json:"code"`
	RequestID string       `json:"requestId,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
}

// FieldError describes one invalid input field.
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// New builds a problem for key, translated to the locale stored in the request context.
func New(r *http.Request, status int, key i18n.Key) Problem {
	msg := i18n.T(i18n.FromContext(r.Context()), key)
	return Problem{
		Type:      typePrefix + string(key),
		Title:     msg.Title,
		Status:    status,
		Detail:    msg.Detail,
		Instance:  r.URL.Path,
		Code:      string(key),
		RequestID: observability.RequestIDFrom(r.Context()),
	}
}

// Write sends a problem response for key with the given status.
func Write(w http.ResponseWriter, r *http.Request, status int, key i18n.Key) {
	WriteProblem(w, r, New(r, status, key))
}

// WriteProblem sends p as the response.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	h := w.Header()
	h.Set("Content-Type", ContentType)
	h.Set("Content-Language", string(i18n.FromContext(r.Context())))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
