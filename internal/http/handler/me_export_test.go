package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// blobExporter writes size random bytes (which do not compress) to one
// file, then fails with err when set.
type blobExporter struct {
	size int
	err  error
}

func (b blobExporter) ExportUserData(_ context.Context, _ string, a authuc.Archive) error {
	w, err := a.Create("blob.json")
	if err != nil {
		return err
	}
	r := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, b.size)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	return b.err
}

var exportedAt = time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)

func TestExportMeRoute(t *testing.T) {
	a := &fakeAccount{export: authuc.DataExport{UserID: "u1", At: exportedAt, Exporters: []authuc.UserDataExporter{blobExporter{size: 10}}}}
	h := newAccountHandler(a)

	w := serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"secret","code":"123456"}`, asUser))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, authuc.ExportDataInput{Password: "secret", Code: "123456"}, a.exported)
	assert.Equal(t, "s1", a.principal.SessionID)
	assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="monthra-data-2026-11-15.zip"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	require.NoError(t, err)
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	assert.Equal(t, []string{authuc.ManifestFile, "blob.json"}, names)
	rc, err := zr.File[1].Open()
	require.NoError(t, err)
	blob, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Len(t, blob, 10)
}

func TestExportMeRouteErrors(t *testing.T) {
	a := &fakeAccount{}
	h := newAccountHandler(a)

	w := serve(h.Export, request(http.MethodPost, "/api/v1/me/export", ``, asUser))
	assert.Equal(t, http.StatusBadRequest, w.Code, "the password is asked in the body")
	w = serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{}`, asUser))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Empty(t, a.exported.Password)

	a.err = authuc.ErrPasswordIncorrect
	w = serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"wrong"}`, asUser))
	assert.Equal(t, http.StatusForbidden, w.Code, "a refused step-up keeps the session")
	assert.Equal(t, "password_incorrect", problemOf(t, w).Code)
	a.err = authuc.ErrMFACodeInvalid
	w = serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"secret"}`, asUser))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "mfa_code_invalid", problemOf(t, w).Code)
	a.err = nil

	// A read that fails before anything is sent answers a problem, without
	// the download's headers.
	a.export = authuc.DataExport{UserID: "u1", At: exportedAt, Exporters: []authuc.UserDataExporter{blobExporter{size: 10, err: errors.New("boom")}}}
	w = serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"secret"}`, asUser))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	assert.Empty(t, w.Header().Get("Content-Disposition"))

	// Once the archive started, a failure aborts the response.
	a.export.Exporters = []authuc.UserDataExporter{blobExporter{size: 64 << 10, err: errors.New("boom")}}
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"secret"}`, asUser))
	})

	w = serve(h.Export, request(http.MethodPost, "/api/v1/me/export", `{"password":"secret"}`))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
