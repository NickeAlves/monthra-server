//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/config"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

const webOrigin = "http://localhost:3000"

var tokenInLink = regexp.MustCompile(`#token=([A-Za-z0-9_-]{43})`)

type response struct {
	status int
	header http.Header
	body   map[string]any
}

func (r response) str(key string) string {
	s, _ := r.body[key].(string)
	return s
}

func call(t *testing.T, client *http.Client, method, url string, body any, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := response{status: resp.StatusCode, header: resp.Header}
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out.body), string(raw))
	}
	return out
}

// startApp runs the whole service against real MongoDB, Redis and Mailpit.
func startApp(t *testing.T) (api string, mail testutil.Mailpit, mongoURI string, metricsURL string) {
	t.Helper()
	_, redisAddr := testutil.StartRedis(t)
	mongoURI = testutil.StartMongo(t)
	mail = testutil.StartMailpit(t)
	for k, v := range map[string]string{
		"MONGO_URI":             mongoURI,
		"REDIS_ADDR":            redisAddr,
		"CSRF_SECRET":           base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
		"JWT_SIGNING_KEYS":      "k1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32)),
		"JWT_ACTIVE_KID":        "k1",
		"HTTP_ADDR":             freeAddr(t),
		"METRICS_ADDR":          freeAddr(t),
		"CORS_ALLOWED_ORIGINS":  webOrigin,
		"APP_PUBLIC_URL":        webOrigin,
		"SMTP_HOST":             mail.SMTPHost,
		"SMTP_PORT":             strconv.Itoa(mail.SMTPPort),
		"SMTP_TLS":              "none",
		"SMTP_FROM":             "Monthra <no-reply@monthra.example>",
		"HIBP_ENABLED":          "false", // no third-party calls from CI
		"RATE_LIMIT_AUTH_IP":    "1000/m",
		"RATE_LIMIT_AUTH_EMAIL": "1000/m",
		"REFRESH_REUSE_GRACE":   "1s",
	} {
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.DiscardHandler), "test") }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	api = "http://" + cfg.HTTP.Addr
	waitHealthy(t, api)
	return api, mail, mongoURI, "http://" + cfg.HTTP.MetricsAddr + "/metrics"
}

func TestAuthFlowEndToEnd(t *testing.T) {
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "ana@example.com"
	password := strings.Repeat("correct horse ", 2)
	creds := map[string]string{"email": addr, "password": password}

	// Sign up, then the login waits for the email to be confirmed.
	r := call(t, native, http.MethodPost, api+"/api/v1/auth/register", map[string]string{"name": "Ana", "email": addr, "password": password}, nil)
	require.Equal(t, http.StatusAccepted, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusForbidden, r.status)
	assert.Equal(t, "email_not_verified", r.str("code"))

	msg := mail.WaitForEmail(t, addr, "Confirme seu e-mail")
	m := tokenInLink.FindStringSubmatch(msg.Text)
	require.Len(t, m, 2, msg.Text)
	assert.Contains(t, msg.Text, webOrigin+"/verify-email#token=")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/verify-email", map[string]string{"token": m[1]}, nil)
	require.Equal(t, http.StatusNoContent, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/verify-email", map[string]string{"token": m[1]}, nil)
	assert.Equal(t, http.StatusBadRequest, r.status, "single use")

	// Registering the address again changes nothing and warns the owner.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/register", map[string]string{"name": "Eve", "email": addr, "password": strings.Repeat("attacker ", 3)}, nil)
	require.Equal(t, http.StatusAccepted, r.status)
	mail.WaitForEmail(t, addr, "Tentativa de cadastro")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": strings.Repeat("attacker ", 3)}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status)
	assert.Equal(t, "invalid_credentials", r.str("code"))

	// Native login and rotation.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Bearer", r.str("tokenType"))
	assert.Empty(t, r.header.Values("Set-Cookie"), "native clients get no cookies")
	rt1 := r.str("refreshToken")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": rt1}, nil)
	require.Equal(t, http.StatusOK, r.status)
	rt2 := r.str("refreshToken")
	require.NotEqual(t, rt1, rt2)

	// Two tabs refreshing at once: one rotates, the other is told to retry.
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			statuses[i] = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": rt2}, nil).status
		})
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, statuses)

	// After the grace window, an old token is a stolen one: the session dies.
	time.Sleep(1200 * time.Millisecond)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": rt1}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": rt2}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "every token of the session is dead")

	// Logout denylists the access token and ends the session.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", creds, nil)
	require.Equal(t, http.StatusOK, r.status)
	bearer := map[string]string{"Authorization": "Bearer " + r.str("accessToken")}
	rt := r.str("refreshToken")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/logout", nil, bearer)
	require.Equal(t, http.StatusNoContent, r.status)
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/logout", nil, bearer)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the access token is denylisted")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/refresh", map[string]string{"refreshToken": rt}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status)

	// Audit trail, without the address.
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	var logs []bson.M
	cur, err := client.Database("monthra").Collection(repomongo.CollAuditLogs).Find(ctx, bson.D{})
	require.NoError(t, err)
	require.NoError(t, cur.All(ctx, &logs))
	events := map[string]int{}
	for _, l := range logs {
		events[l["event"].(string)]++
		raw, err := bson.MarshalExtJSON(l, false, false)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), addr)
		assert.NotContains(t, string(raw), password)
	}
	assert.Equal(t, map[string]int{"register": 1, "email_verified": 1, "login_failure": 1, "login_success": 2, "refresh_reuse_detected": 1, "logout": 1}, events)

	_, metrics := get(t, metricsURL)
	for _, line := range []string{
		`monthra_auth_logins_total{result="success"} 2`,
		`monthra_auth_refresh_reuse_total 1`,
		`monthra_email_sent_total{result="sent",template="verify_email"} 1`,
		`monthra_email_sent_total{result="sent",template="register_attempt"} 1`,
	} {
		assert.True(t, strings.Contains(metrics, line), "missing metric %s", line)
	}
}

func TestAuthFlowFromABrowser(t *testing.T) {
	api, mail, _, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "web@example.com"
	password := strings.Repeat("battery staple ", 2)
	r := call(t, native, http.MethodPost, api+"/api/v1/auth/register", map[string]string{"name": "Web", "email": addr, "password": password}, map[string]string{"Accept-Language": "en-US"})
	require.Equal(t, http.StatusAccepted, r.status)
	m := tokenInLink.FindStringSubmatch(mail.WaitForEmail(t, addr, "Confirm your email").Text)
	require.Len(t, m, 2)
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodPost, api+"/api/v1/auth/verify-email", map[string]string{"token": m[1]}, nil).status)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	r = call(t, browser, http.MethodGet, api+"/api/v1/auth/csrf", nil, map[string]string{"Origin": webOrigin})
	require.Equal(t, http.StatusOK, r.status)
	headers := map[string]string{"Origin": webOrigin, "X-CSRF-Token": r.str("csrfToken")}

	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": password}, map[string]string{"Origin": webOrigin})
	assert.Equal(t, http.StatusForbidden, r.status, "browsers need the CSRF token")
	assert.Equal(t, "csrf_invalid", r.str("code"))

	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": password}, headers)
	require.Equal(t, http.StatusOK, r.status)
	assert.Empty(t, r.str("accessToken"), "no token in the body")
	assert.Empty(t, r.str("refreshToken"))
	assert.InDelta(t, 900, r.body["expiresIn"], 1, "issued-at is truncated to the second")
	setCookies := strings.Join(r.header.Values("Set-Cookie"), "\n")
	assert.Contains(t, setCookies, "monthra_at=")
	assert.Contains(t, setCookies, "monthra_rt=")
	assert.Contains(t, setCookies, "Path=/api/v1/auth/refresh")
	assert.Contains(t, setCookies, "HttpOnly")
	assert.Contains(t, setCookies, "SameSite=Strict")

	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/refresh", nil, headers)
	require.Equal(t, http.StatusOK, r.status, "the refresh cookie reaches its route")
	assert.Contains(t, strings.Join(r.header.Values("Set-Cookie"), "\n"), "monthra_rt=")

	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/logout", nil, headers)
	require.Equal(t, http.StatusNoContent, r.status, "the access cookie authenticates")
	r = call(t, browser, http.MethodPost, api+"/api/v1/auth/refresh", nil, headers)
	assert.Equal(t, http.StatusUnauthorized, r.status, "logout cleared the cookies")
}
