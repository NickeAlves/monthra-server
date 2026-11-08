package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SHA-1 of "password" is 5BAA61E4C9B93F3F0682250B6CF8331B7EE68FD8.
const (
	pwnedPrefix = "5BAA6"
	pwnedSuffix = "1E4C9B93F3F0682250B6CF8331B7EE68FD8"
)

func rangeServer(t *testing.T, status int, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	var seen http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestHIBPFindsBreachedPasswords(t *testing.T) {
	srv, seen := rangeServer(t, http.StatusOK, "0018A45C4D1DEF81644B54AB7F969B88D65:1\r\n"+pwnedSuffix+":9545824\r\n")
	breached, err := NewHIBP(srv.Client(), srv.URL+"/range/").Breached(context.Background(), "password")
	require.NoError(t, err)
	assert.True(t, breached)
	assert.Equal(t, "/range/"+pwnedPrefix, seen.URL.Path, "only the 5-character prefix is sent")
	assert.Equal(t, "true", seen.Header.Get("Add-Padding"))
	assert.Equal(t, "monthra-server", seen.Header.Get("User-Agent"))
}

func TestHIBPIgnoresMissesAndPadding(t *testing.T) {
	for name, body := range map[string]string{
		"miss":    "0018A45C4D1DEF81644B54AB7F969B88D65:1\r\n",
		"padding": pwnedSuffix + ":0\r\n",
		"empty":   "",
	} {
		srv, _ := rangeServer(t, http.StatusOK, body)
		breached, err := NewHIBP(srv.Client(), srv.URL+"/range/").Breached(context.Background(), "password")
		require.NoError(t, err, name)
		assert.False(t, breached, name)
	}
}

func TestHIBPReportsFailures(t *testing.T) {
	srv, _ := rangeServer(t, http.StatusServiceUnavailable, "")
	_, err := NewHIBP(srv.Client(), srv.URL+"/range/").Breached(context.Background(), "password")
	assert.ErrorContains(t, err, "503")

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = NewHIBP(nil, slow.URL+"/range/").Breached(ctx, "password")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = NewHIBP(nil, "http://[::1]:namedport/").Breached(context.Background(), "password")
	assert.Error(t, err)
}

func TestNoBreachCheck(t *testing.T) {
	breached, err := NoBreachCheck{}.Breached(context.Background(), "password")
	assert.NoError(t, err)
	assert.False(t, breached)
}
