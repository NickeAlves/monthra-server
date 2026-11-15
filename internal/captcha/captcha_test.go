package captcha

import (
	"bytes"
	"context"
	"log/slog"
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
	ok, err := v.Verify(context.Background(), Challenge{})
	assert.True(t, ok)
	assert.NoError(t, err)
}

// siteverify answers like Cloudflare: tokens "good-<action>" pass with that
// action on app.monthra.example, "elsewhere" passes on another host, and
// anything else fails.
func siteverify(t *testing.T, form *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if form != nil {
			*form = map[string]string{"secret": r.PostForm.Get("secret"), "response": r.PostForm.Get("response"), "remoteip": r.PostForm.Get("remoteip")}
		}
		token := r.PostForm.Get("response")
		switch {
		case strings.HasPrefix(token, "good-"):
			_, _ = w.Write([]byte(`{"success":true,"hostname":"App.Monthra.example","action":"` + strings.TrimPrefix(token, "good-") + `"}`))
		case token == "elsewhere":
			_, _ = w.Write([]byte(`{"success":true,"hostname":"evil.example","action":"login"}`))
		case token == "dummy":
			// Cloudflare's testing secrets answer with no action.
			_, _ = w.Write([]byte(`{"success":true,"hostname":"example.com","metadata":{"result_with_testing_key":true}}`))
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTurnstile(t *testing.T) {
	var form map[string]string
	srv := siteverify(t, &form)
	v := NewTurnstile(TurnstileOptions{Secret: "site-secret", URL: srv.URL, Client: srv.Client()})
	assert.True(t, v.Enabled())

	ok, err := v.Verify(context.Background(), Challenge{Token: "good-login", Action: ActionLogin, RemoteIP: "198.51.100.7"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, map[string]string{"secret": "site-secret", "response": "good-login", "remoteip": "198.51.100.7"}, form)

	ok, err = v.Verify(context.Background(), Challenge{Token: "bad", Action: ActionLogin})
	require.NoError(t, err)
	assert.False(t, ok)

	form = nil
	ok, err = v.Verify(context.Background(), Challenge{Action: ActionLogin})
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = v.Verify(context.Background(), Challenge{Token: strings.Repeat("a", maxTokenLength+1), Action: ActionLogin})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, form, "empty or oversized tokens never reach Cloudflare")
}

func TestTurnstileWithoutHostnamesSkipsHostnameAndAction(t *testing.T) {
	srv := siteverify(t, nil)
	v := NewTurnstile(TurnstileOptions{Secret: "s", URL: srv.URL})
	for _, token := range []string{"elsewhere", "dummy", "good-register"} {
		ok, err := v.Verify(context.Background(), Challenge{Token: token, Action: ActionLogin})
		require.NoError(t, err)
		assert.True(t, ok, "%s passes: local runs use Cloudflare's testing keys", token)
	}
}

func TestTurnstileChecksHostnameAndAction(t *testing.T) {
	var logs bytes.Buffer
	srv := siteverify(t, nil)
	v := NewTurnstile(TurnstileOptions{
		Secret: "s", URL: srv.URL, Hostnames: []string{"app.monthra.example"},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	for name, tc := range map[string]struct {
		token, action string
		want          bool
	}{
		"same form on our host":  {"good-login", ActionLogin, true},
		"sign-up form":           {"good-register", ActionRegister, true},
		"solved on another form": {"good-register", ActionLogin, false},
		"solved on another site": {"elsewhere", ActionLogin, false},
		"testing key answer":     {"dummy", ActionLogin, false},
		"refused by Cloudflare":  {"bad", ActionLogin, false},
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := v.Verify(context.Background(), Challenge{Token: tc.token, Action: tc.action})
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
		})
	}
	assert.Contains(t, logs.String(), "captcha token from another form")
	assert.Contains(t, logs.String(), "captcha token from another hostname")
	assert.Contains(t, logs.String(), "invalid-input-response")
	for _, token := range []string{"good-login", "good-register", "elsewhere", "dummy"} {
		assert.NotContains(t, logs.String(), `"`+token+`"`, "tokens never reach the logs")
	}
}

func TestTurnstileFailures(t *testing.T) {
	c := Challenge{Token: "tok", Action: ActionLogin}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(down.Close)
	_, err := NewTurnstile(TurnstileOptions{Secret: "s", URL: down.URL}).Verify(context.Background(), c)
	assert.ErrorContains(t, err, "502")

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(garbage.Close)
	_, err = NewTurnstile(TurnstileOptions{Secret: "s", URL: garbage.URL}).Verify(context.Background(), c)
	assert.ErrorContains(t, err, "decode")

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = NewTurnstile(TurnstileOptions{Secret: "s", URL: slow.URL}).Verify(ctx, c)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = NewTurnstile(TurnstileOptions{Secret: "s", URL: "http://[::1]:namedport"}).Verify(context.Background(), c)
	assert.Error(t, err)
}
