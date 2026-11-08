//go:build integration

package app

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/testutil"
)

// signUp registers addr and confirms it through the emailed link.
func signUp(t *testing.T, api string, mail testutil.Mailpit, addr, password string) {
	t.Helper()
	native := &http.Client{Timeout: 10 * time.Second}
	r := call(t, native, http.MethodPost, api+"/api/v1/auth/register", map[string]string{"name": "Ana", "email": addr, "password": password}, nil)
	require.Equal(t, http.StatusAccepted, r.status)
	m := tokenInLink.FindStringSubmatch(mail.WaitForEmail(t, addr, "Confirme seu e-mail").Text)
	require.Len(t, m, 2)
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodPost, api+"/api/v1/auth/verify-email", map[string]string{"token": m[1]}, nil).status)
}

func TestPasswordRecoveryEndToEnd(t *testing.T) {
	api, mail, _, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "reset@example.com"
	oldPassword, newPassword := strings.Repeat("old horse ", 2), strings.Repeat("new staple ", 2)
	signUp(t, api, mail, addr, oldPassword)

	r := call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": oldPassword}, nil)
	require.Equal(t, http.StatusOK, r.status)
	bearer := map[string]string{"Authorization": "Bearer " + r.str("accessToken")}
	refresh := r.str("refreshToken")

	// Unknown addresses get the same answer and no email.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/forgot-password", map[string]string{"email": "nobody@example.com"}, nil)
	assert.Equal(t, http.StatusAccepted, r.status)

	r = call(t, native, http.MethodPost, api+"/api/v1/auth/forgot-password", map[string]string{"email": addr}, nil)
	require.Equal(t, http.StatusAccepted, r.status)
	msg := mail.WaitForEmail(t, addr, "Redefina sua senha")
	assert.Contains(t, msg.Text, webOrigin+"/reset-password#token=")
	assert.Contains(t, msg.Text, "15 minutos")
	m := tokenInLink.FindStringSubmatch(msg.Text)
	require.Len(t, m, 2)

	r = call(t, native, http.MethodPost, api+"/api/v1/auth/reset-password", map[string]string{"token": m[1], "password": "short"}, nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.status, "the policy applies")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/reset-password", map[string]string{"token": m[1], "password": newPassword}, nil)
	require.Equal(t, http.StatusNoContent, r.status, "a refused password did not burn the link")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/reset-password", map[string]string{"token": m[1], "password": newPassword}, nil)
	assert.Equal(t, http.StatusBadRequest, r.status, "single use")
	assert.Equal(t, "token_invalid", r.str("code"))
	mail.WaitForEmail(t, addr, "Sua senha do Monthra foi alterada")

	// Every session from before the reset is gone.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/logout", nil, bearer)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the old access token is denied")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": refresh}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the old refresh token is dead")

	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": oldPassword}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": newPassword}, nil)
	assert.Equal(t, http.StatusOK, r.status)

	_, metrics := get(t, metricsURL)
	for _, line := range []string{
		`monthra_auth_password_resets_total 1`,
		`monthra_email_sent_total{result="sent",template="reset_password"} 1`,
		`monthra_email_sent_total{result="sent",template="password_changed"} 1`,
	} {
		assert.True(t, strings.Contains(metrics, line), "missing metric %s", line)
	}
}
