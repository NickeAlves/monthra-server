//go:build integration

package app

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func TestMFAEndToEnd(t *testing.T) {
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "mfa@example.com"
	password := strings.Repeat("correct horse ", 2)
	creds := map[string]string{"email": addr, "password": password}
	signUp(t, api, mail, addr, password)

	r := call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	bearer := map[string]string{"Authorization": "Bearer " + r.str("accessToken")}

	// Setup asks for the password again, then enable confirms a first code.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/setup", map[string]string{"password": "wrong password!"}, bearer)
	require.Equal(t, http.StatusForbidden, r.status)
	assert.Equal(t, "password_incorrect", r.str("code"))
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/setup", map[string]string{"password": password}, bearer)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	secret := r.str("secret")
	assert.Contains(t, r.str("otpauthUri"), "issuer=Monthra%20%28local%29")

	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/enable", map[string]string{"code": code}, bearer)
	require.Equal(t, http.StatusOK, r.status)
	raw, _ := r.body["recoveryCodes"].([]any)
	require.Len(t, raw, 10)
	codes := make([]string, len(raw))
	for i, c := range raw {
		codes[i] = c.(string)
	}
	mail.WaitForEmail(t, addr, "Verificação em duas etapas ativada")

	// At rest the secret is sealed and the recovery codes are hashes.
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	var doc struct {
		MFA struct {
			Secret        string   `bson:"secret"`
			RecoveryCodes []string `bson:"recovery_codes"`
		} `bson:"mfa"`
	}
	require.NoError(t, client.Database("monthra").Collection(repomongo.CollUsers).FindOne(ctx, bson.D{{Key: "email", Value: addr}}).Decode(&doc))
	assert.True(t, strings.HasPrefix(doc.MFA.Secret, "e1:"), doc.MFA.Secret)
	assert.NotContains(t, doc.MFA.Secret, secret)
	require.Len(t, doc.MFA.RecoveryCodes, 10)
	for _, c := range codes {
		assert.NotContains(t, doc.MFA.RecoveryCodes, c)
	}

	// The password alone no longer opens a session.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, true, r.body["mfaRequired"])
	assert.Empty(t, r.str("accessToken"))
	mfaToken := r.str("mfaToken")

	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": mfaToken, "code": "000000"}, nil)
	require.Equal(t, http.StatusForbidden, r.status)
	assert.Equal(t, "mfa_code_invalid", r.str("code"))
	// The enable spent the current step; the next one is within the drift window.
	next, err := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": mfaToken, "code": next}, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.NotEmpty(t, r.str("accessToken"))
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": mfaToken, "code": next}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the challenge is spent")
	assert.Equal(t, "mfa_challenge_invalid", r.str("code"))

	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": r.str("mfaToken"), "code": next}, nil)
	assert.Equal(t, http.StatusForbidden, r.status, "a TOTP code works once")

	// Recovery codes work once each.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": r.str("mfaToken"), "code": strings.ToUpper(codes[0])}, nil)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": r.str("mfaToken"), "code": codes[0]}, nil)
	assert.Equal(t, http.StatusForbidden, r.status)

	// Two verifications of one challenge at once: one session only.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	shared := r.str("mfaToken")
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			statuses[i] = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": shared, "code": codes[1+i]}, nil).status
		})
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusUnauthorized}, statuses)

	// From a browser, the session cookies come with the second step only.
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	r = call(t, browser, http.MethodGet, api+"/api/v1/auth/csrf", nil, map[string]string{"Origin": webOrigin})
	require.Equal(t, http.StatusOK, r.status)
	headers := map[string]string{"Origin": webOrigin, "X-CSRF-Token": r.str("csrfToken")}
	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/login", creds, headers)
	require.Equal(t, http.StatusOK, r.status)
	assert.NotContains(t, strings.Join(r.header.Values("Set-Cookie"), "\n"), "monthra_at=")
	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/mfa/verify", map[string]string{"mfaToken": r.str("mfaToken"), "code": codes[3]}, headers)
	require.Equal(t, http.StatusOK, r.status)
	assert.Empty(t, r.str("accessToken"))
	assert.Contains(t, strings.Join(r.header.Values("Set-Cookie"), "\n"), "monthra_at=")

	// Turning MFA off takes the password and a code.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/disable", map[string]string{"password": password, "code": "000000"}, bearer)
	assert.Equal(t, http.StatusForbidden, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/disable", map[string]string{"password": password, "code": codes[4]}, bearer)
	require.Equal(t, http.StatusNoContent, r.status)
	mail.WaitForEmail(t, addr, "Verificação em duas etapas desativada")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/mfa/disable", map[string]string{"password": password, "code": codes[5]}, bearer)
	assert.Equal(t, http.StatusConflict, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.NotEmpty(t, r.str("accessToken"), "the password is enough again")

	_, metrics := get(t, metricsURL)
	for _, line := range []string{
		`monthra_auth_mfa_total{event="enabled"} 1`,
		`monthra_auth_mfa_total{event="disabled"} 1`,
		`monthra_auth_logins_total{result="mfa_required"} 6`,
		`monthra_email_sent_total{result="sent",template="mfa_enabled"} 1`,
	} {
		assert.True(t, strings.Contains(metrics, line), "missing metric %s", line)
	}
}
