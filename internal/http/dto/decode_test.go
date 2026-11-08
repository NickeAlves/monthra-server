package dto

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func request(contentType, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func decodeErr(t *testing.T, err error) *Error {
	t.Helper()
	var de *Error
	require.ErrorAs(t, err, &de)
	return de
}

func TestDecodeAcceptsAValidObject(t *testing.T) {
	var req LoginRequest
	require.NoError(t, Decode(request("application/json; charset=utf-8", `{"email":"a@b.c","password":"pw"}`), &req))
	assert.Equal(t, LoginRequest{Email: "a@b.c", Password: "pw"}, req)
}

func TestDecodeRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType, body string
		status            int
		key               i18n.Key
	}{
		"form post":         {"application/x-www-form-urlencoded", "email=a", http.StatusUnsupportedMediaType, i18n.ErrUnsupportedMediaType},
		"no content type":   {"", `{}`, http.StatusUnsupportedMediaType, i18n.ErrUnsupportedMediaType},
		"text/plain":        {"text/plain", `{}`, http.StatusUnsupportedMediaType, i18n.ErrUnsupportedMediaType},
		"malformed json":    {"application/json", `{"email":`, http.StatusBadRequest, i18n.ErrBadRequest},
		"empty body":        {"application/json", ``, http.StatusBadRequest, i18n.ErrBadRequest},
		"unknown field":     {"application/json", `{"email":"a","password":"b","isAdmin":true}`, http.StatusBadRequest, i18n.ErrBadRequest},
		"mongo operator":    {"application/json", `{"email":{"$ne":null},"password":"b"}`, http.StatusUnprocessableEntity, i18n.ErrValidationFailed},
		"operator as a key": {"application/json", `{"$where":"1","email":"a","password":"b"}`, http.StatusBadRequest, i18n.ErrBadRequest},
		"trailing data":     {"application/json", `{"email":"a","password":"b"} {"x":1}`, http.StatusBadRequest, i18n.ErrBadRequest},
		"array":             {"application/json", `[]`, http.StatusBadRequest, i18n.ErrBadRequest},
		"missing fields":    {"application/json", `{}`, http.StatusUnprocessableEntity, i18n.ErrValidationFailed},
	} {
		var req LoginRequest
		de := decodeErr(t, Decode(request(tc.contentType, tc.body), &req))
		assert.Equal(t, tc.status, de.Status, name)
		assert.Equal(t, tc.key, de.Key, name)
		assert.Equal(t, string(tc.key), de.Error(), name)
	}
}

func TestDecodeReportsFieldsByJSONName(t *testing.T) {
	var req RegisterRequest
	de := decodeErr(t, Decode(request("application/json", `{"name":"","email":"`+strings.Repeat("a", 300)+`","password":"x"}`), &req))
	assert.Equal(t, []domain.FieldError{{Field: "name", Code: "required"}, {Field: "email", Code: "too_long"}}, de.Fields)

	var login LoginRequest
	de = decodeErr(t, Decode(request("application/json", `{"email":42,"password":"x"}`), &login))
	assert.Equal(t, []domain.FieldError{{Field: "email", Code: "invalid"}}, de.Fields)
}

func TestDecodeReportsOversizedBodies(t *testing.T) {
	r := request("application/json", `{"email":"`+strings.Repeat("a", 100)+`"}`)
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)
	var req LoginRequest
	de := decodeErr(t, Decode(r, &req))
	assert.Equal(t, http.StatusRequestEntityTooLarge, de.Status)
}

func TestValidateCodes(t *testing.T) {
	type sample struct {
		A string `json:"a" validate:"min=3"`
		B string `json:"b" validate:"email"`
		C string `json:"-" validate:"required"`
	}
	de := decodeErr(t, Validate(sample{A: "x", B: "nope"}))
	assert.Equal(t, []domain.FieldError{{Field: "a", Code: "too_short"}, {Field: "b", Code: "invalid"}, {Field: "C", Code: "required"}}, de.Fields)
	assert.Equal(t, http.StatusBadRequest, decodeErr(t, Validate(42)).Status, "not a struct")
}
