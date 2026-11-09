//go:build integration

package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// signIn logs in a native client and returns its Authorization header.
func signIn(t *testing.T, client *http.Client, api, addr, password string) map[string]string {
	t.Helper()
	r := call(t, client, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": password}, nil)
	require.Equal(t, http.StatusOK, r.status)
	return map[string]string{"Authorization": "Bearer " + r.str("accessToken")}
}

// currentSession returns the id of the session behind auth.
func currentSession(t *testing.T, client *http.Client, api string, auth map[string]string) string {
	t.Helper()
	r := call(t, client, http.MethodGet, api+"/api/v1/me/sessions", nil, auth)
	require.Equal(t, http.StatusOK, r.status)
	list, _ := r.body["sessions"].([]any)
	for _, raw := range list {
		s, _ := raw.(map[string]any)
		if s["current"] == true {
			return s["id"].(string)
		}
	}
	t.Fatal("no current session")
	return ""
}

func TestAccountEndToEnd(t *testing.T) {
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr, other = "account@example.com", "other@example.com"
	password, newPassword := strings.Repeat("correct horse ", 2), strings.Repeat("battery staple ", 2)
	signUp(t, api, mail, addr, password)
	signUp(t, api, mail, other, password)

	deviceA := signIn(t, native, api, addr, password)
	deviceB := signIn(t, native, api, addr, password)
	bob := signIn(t, native, api, other, password)

	// Profile.
	r := call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceA)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, addr, r.str("email"))
	assert.Equal(t, true, r.body["emailVerified"])
	assert.Equal(t, map[string]any{"enabled": false, "recoveryCodesRemaining": float64(0)}, r.body["mfa"])
	assert.Equal(t, map[string]any{"locale": "pt-BR", "theme": "system", "currency": "BRL", "budgetAlerts": true}, r.body["preferences"])
	userID := r.str("id")
	require.Len(t, userID, 24)

	r = call(t, native, http.MethodPatch, api+"/api/v1/me", map[string]string{"name": " Ana Maria "}, deviceA)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Ana Maria", r.str("name"))
	r = call(t, native, http.MethodPatch, api+"/api/v1/me", map[string]any{"name": "x", "email": "evil@example.com"}, deviceA)
	assert.Equal(t, http.StatusBadRequest, r.status, "only whitelisted fields")

	// The stored locale wins over Accept-Language, on every device.
	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]string{"locale": "en-US", "currency": "EUR"}, deviceA)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, map[string]any{"locale": "en-US", "theme": "system", "currency": "EUR", "budgetAlerts": true}, r.body)
	withPtBR := map[string]string{"Authorization": deviceB["Authorization"], "Accept-Language": "pt-BR"}
	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]string{"theme": "sepia"}, withPtBR)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, "en-US", r.header.Get("Content-Language"))
	assert.Equal(t, "Invalid data", r.str("title"))
	r = call(t, native, http.MethodGet, api+"/api/v1/me/preferences", nil, deviceB)
	assert.Equal(t, "EUR", r.str("currency"))

	// Sessions: one user cannot see or end another's.
	r = call(t, native, http.MethodGet, api+"/api/v1/me/sessions", nil, deviceA)
	require.Equal(t, http.StatusOK, r.status)
	sessions, _ := r.body["sessions"].([]any)
	require.Len(t, sessions, 2)
	sidA, sidB, bobSID := currentSession(t, native, api, deviceA), currentSession(t, native, api, deviceB), currentSession(t, native, api, bob)
	assert.NotEqual(t, sidA, sidB)

	r = call(t, native, http.MethodDelete, api+"/api/v1/me/sessions/"+bobSID, nil, deviceA)
	assert.Equal(t, http.StatusNotFound, r.status, "no IDOR")
	assert.Equal(t, http.StatusOK, call(t, native, http.MethodGet, api+"/api/v1/me", nil, bob).status, "bob is still signed in")
	r = call(t, native, http.MethodDelete, api+"/api/v1/me/sessions/not-an-id", nil, deviceA)
	assert.Equal(t, http.StatusNotFound, r.status)

	r = call(t, native, http.MethodDelete, api+"/api/v1/me/sessions/"+sidB, nil, deviceA)
	require.Equal(t, http.StatusNoContent, r.status)
	assert.Equal(t, http.StatusUnauthorized, call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceB).status, "device B is signed out at once")

	deviceC := signIn(t, native, api, addr, password)
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, api+"/api/v1/me/sessions", nil, deviceA).status)
	assert.Equal(t, http.StatusUnauthorized, call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceC).status)
	assert.Equal(t, http.StatusOK, call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceA).status, "the caller stays")

	// Password change keeps only the current session.
	deviceD := signIn(t, native, api, addr, password)
	r = call(t, native, http.MethodPost, api+"/api/v1/me/password", map[string]string{"currentPassword": "wrong password!", "newPassword": newPassword}, deviceA)
	require.Equal(t, http.StatusForbidden, r.status)
	assert.Equal(t, "password_incorrect", r.str("code"))
	r = call(t, native, http.MethodPost, api+"/api/v1/me/password", map[string]string{"currentPassword": password, "newPassword": newPassword}, deviceA)
	require.Equal(t, http.StatusNoContent, r.status)
	assert.Equal(t, http.StatusUnauthorized, call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceD).status)
	assert.Equal(t, http.StatusOK, call(t, native, http.MethodGet, api+"/api/v1/me", nil, deviceA).status)
	mail.WaitForEmail(t, addr, "Your Monthra password was changed")
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": password}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the old password is gone")

	// Deletion asks for the password, then erases everything but the audit trail.
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, deviceA)
	require.Equal(t, http.StatusForbidden, r.status)
	deviceE := signIn(t, native, api, addr, newPassword)
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": newPassword}, deviceA)
	require.Equal(t, http.StatusNoContent, r.status)
	for name, auth := range map[string]map[string]string{"caller": deviceA, "other device": deviceE} {
		assert.Equal(t, http.StatusUnauthorized, call(t, native, http.MethodGet, api+"/api/v1/me", nil, auth).status, name)
	}
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/login", map[string]string{"email": addr, "password": newPassword}, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status)
	mail.WaitForEmail(t, addr, "Your Monthra account was deleted")
	assert.Equal(t, http.StatusOK, call(t, native, http.MethodGet, api+"/api/v1/me", nil, bob).status, "other accounts are untouched")

	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra")
	oid, err := bson.ObjectIDFromHex(userID)
	require.NoError(t, err)
	n, err := db.Collection(repomongo.CollUsers).CountDocuments(ctx, bson.D{{Key: "_id", Value: oid}})
	require.NoError(t, err)
	assert.Zero(t, n, "users")
	for _, coll := range []string{repomongo.CollSessions, repomongo.CollRefreshTokens, repomongo.CollAuthTokens} {
		n, err := db.Collection(coll).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
		require.NoError(t, err)
		assert.Zero(t, n, coll)
	}
	n, err = db.Collection(repomongo.CollAuditLogs).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}, {Key: "event", Value: "account_deleted"}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the deletion is audited")
	var entry bson.Raw
	require.NoError(t, db.Collection(repomongo.CollAuditLogs).FindOne(ctx, bson.D{{Key: "event", Value: "account_deleted"}}).Decode(&entry))
	assert.NotContains(t, entry.String(), addr, "no address in the audit log")

	// The address is free again.
	r = call(t, native, http.MethodPost, api+"/api/v1/auth/register", map[string]string{"name": "Ana", "email": addr, "password": password}, nil)
	require.Equal(t, http.StatusAccepted, r.status)
	mail.WaitForEmail(t, addr, "Confirme seu e-mail")
}
