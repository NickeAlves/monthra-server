// Package dto holds the request and response bodies of the API and the
// strict JSON decoding every handler uses.
package dto

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Error is a request that could not be decoded or validated.
type Error struct {
	Status int
	Key    i18n.Key
	Fields []domain.FieldError
}

func (e *Error) Error() string { return string(e.Key) }

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report fields by their JSON names.
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// Decode reads one JSON object from r into v and validates it. It accepts
// only application/json (a form post cannot reach it without a CORS
// preflight), rejects unknown fields (no mass assignment, no "$" operators),
// trailing data and bodies over the limit.
func Decode(r *http.Request, v any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &Error{Status: http.StatusUnsupportedMediaType, Key: i18n.ErrUnsupportedMediaType}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return decodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &Error{Status: http.StatusBadRequest, Key: i18n.ErrBadRequest}
	}
	return Validate(v)
}

// Validate runs the struct tag rules of v.
func Validate(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return &Error{Status: http.StatusBadRequest, Key: i18n.ErrBadRequest}
	}
	fields := make([]domain.FieldError, len(verrs))
	for i, fe := range verrs {
		fields[i] = domain.FieldError{Field: fe.Field(), Code: codeFor(fe.Tag())}
	}
	return &Error{Status: http.StatusUnprocessableEntity, Key: i18n.ErrValidationFailed, Fields: fields}
}

func codeFor(tag string) string {
	switch tag {
	case "required":
		return domain.CodeRequired
	case "max":
		return domain.CodeTooLong
	case "min":
		return domain.CodeTooShort
	default:
		return domain.CodeInvalid
	}
}

func decodeError(err error) error {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return &Error{Status: http.StatusRequestEntityTooLarge, Key: i18n.ErrPayloadTooLarge}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return &Error{Status: http.StatusUnprocessableEntity, Key: i18n.ErrValidationFailed,
			Fields: []domain.FieldError{{Field: typeErr.Field, Code: domain.CodeInvalid}}}
	}
	return &Error{Status: http.StatusBadRequest, Key: i18n.ErrBadRequest}
}
