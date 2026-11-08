package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisabled(t *testing.T) {
	var v Verifier = Disabled{}
	assert.False(t, v.Enabled())
	ok, err := v.Verify(context.Background(), "", "")
	assert.True(t, ok)
	assert.NoError(t, err)
}

func TestTurnstile(t *testing.T) {
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		form = map[string]string{"secret": r.PostForm.Get("secret"), "response": r.PostForm.Get("response"), "remoteip": r.PostForm.Get("remoteip")}
		if r.PostForm.Get("response") == "good" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	t.Cleanup(srv.Close)
	v := NewTurnstile("site-secret", srv.URL, srv.Client())
	assert.True(t, v.Enabled())

	ok, err := v.Verify(context.Background(), "good", "198.51.100.7")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, map[string]string{"secret": "site-secret", "response": "good", "remoteip": "198.51.100.7"}, form)

	ok, err = v.Verify(context.Background(), "bad", "")
	require.NoError(t, err)
	assert.False(t, ok)

	form = nil
	ok, err = v.Verify(context.Background(), "", "")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = v.Verify(context.Background(), strings.Repeat("a", maxTokenLength+1), "")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, form, "empty or oversized tokens never reach Cloudflare")
}

func TestTurnstileFailures(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(down.Close)
	_, err := NewTurnstile("s", down.URL, nil).Verify(context.Background(), "tok", "")
	assert.ErrorContains(t, err, "502")

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(garbage.Close)
	_, err = NewTurnstile("s", garbage.URL, nil).Verify(context.Background(), "tok", "")
	assert.ErrorContains(t, err, "decode")

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = NewTurnstile("s", slow.URL, nil).Verify(ctx, "tok", "")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = NewTurnstile("s", "http://[::1]:namedport", nil).Verify(context.Background(), "tok", "")
	assert.Error(t, err)
}
