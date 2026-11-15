//go:build integration

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/dataexport"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// postExport sends POST /me/export and returns the raw answer.
func postExport(t *testing.T, client *http.Client, api string, body any, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, api+"/api/v1/me/export", bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, raw
}

func TestDataExportEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	t.Setenv("RATE_LIMIT_DATA_EXPORT_USER", "3/h")
	api, mail, mongoURI, metricsURL := startApp(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	ctx := context.Background()
	native := &http.Client{Timeout: 30 * time.Second}

	// The demo user holds every kind of record; another user's must stay out.
	demo, err := Seed(ctx, cfg, slog.New(slog.DiscardHandler), "")
	require.NoError(t, err)
	const bob = "bob@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, bob, password)
	asBob := signIn(t, native, api, bob, password)
	r := call(t, native, http.MethodPost, api+"/api/v1/accounts", map[string]any{"name": "Bob's", "type": "checking", "currency": "BRL"}, asBob)
	require.Equal(t, http.StatusCreated, r.status, r.body)
	bobAccount := r.str("id")
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asBob)
	bobID := r.str("id")

	asDemo := signIn(t, native, api, demo.Email, demo.Password)
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asDemo)
	demoID := r.str("id")

	status, header, body := postExport(t, native, api, map[string]string{"password": "wrong password!"}, asDemo)
	require.Equal(t, http.StatusForbidden, status, string(body))
	assert.Contains(t, string(body), `"code":"password_incorrect"`)
	assert.Equal(t, "application/problem+json", header.Get("Content-Type"))

	status, header, body = postExport(t, native, api, map[string]string{"password": demo.Password}, asDemo)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Equal(t, "application/zip", header.Get("Content-Type"))
	assert.Regexp(t, `^attachment; filename="monthra-data-\d{4}-\d{2}-\d{2}\.zip"$`, header.Get("Content-Disposition"))
	assert.Equal(t, "no-store", header.Get("Cache-Control"))

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	files := map[string][]byte{}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		require.NoError(t, err)
		files[f.Name], err = io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
	}
	assert.Equal(t, []string{
		authuc.ManifestFile, dataexport.FileProfile, dataexport.FileAccounts, dataexport.FileCategories,
		dataexport.FileTransactions, dataexport.FileRecurrences, dataexport.FileBudgets, dataexport.FileInvestments,
		dataexport.FileInvestmentMovements, dataexport.FileSessions, dataexport.FileAuditLog,
	}, names)

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(files[authuc.ManifestFile], &manifest))
	assert.Equal(t, demoID, manifest["userId"])
	assert.Equal(t, "monthra-data-export", manifest["format"])
	var profile map[string]any
	require.NoError(t, json.Unmarshal(files[dataexport.FileProfile], &profile))
	assert.Equal(t, demo.Email, profile["email"])

	// Each file holds every record of the demo user in its collection.
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database(cfg.Mongo.Database)
	uid, err := bson.ObjectIDFromHex(demoID)
	require.NoError(t, err)
	for file, coll := range map[string]string{
		dataexport.FileAccounts:            repomongo.CollAccounts,
		dataexport.FileCategories:          repomongo.CollCategories,
		dataexport.FileTransactions:        repomongo.CollTransactions,
		dataexport.FileRecurrences:         repomongo.CollRecurrences,
		dataexport.FileBudgets:             repomongo.CollBudgets,
		dataexport.FileInvestments:         repomongo.CollInvestments,
		dataexport.FileInvestmentMovements: repomongo.CollInvestmentMovements,
		dataexport.FileSessions:            repomongo.CollSessions,
		dataexport.FileAuditLog:            repomongo.CollAuditLogs,
	} {
		var records []map[string]any
		require.NoError(t, json.Unmarshal(files[file], &records), file)
		stored, err := db.Collection(coll).CountDocuments(ctx, bson.D{{Key: "user_id", Value: uid}})
		require.NoError(t, err)
		assert.Positive(t, stored, coll)
		assert.Len(t, records, int(stored), file)
		for _, rec := range records {
			assert.NotContains(t, rec, "userId", file)
		}
	}
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(files[dataexport.FileAuditLog], &entries))
	assert.Equal(t, "data_exported", entries[0]["event"], "the export itself, the newest entry")
	assert.Equal(t, "reauth_failure", entries[1]["event"])

	// Nothing secret, nothing of Bob's.
	var user struct {
		PasswordHash string `bson:"password_hash"`
	}
	require.NoError(t, db.Collection(repomongo.CollUsers).FindOne(ctx, bson.D{{Key: "_id", Value: uid}}).Decode(&user))
	require.NotEmpty(t, user.PasswordHash)
	for name, content := range files {
		assert.NotContains(t, string(content), user.PasswordHash, name)
		assert.NotContains(t, string(content), bobID, name)
		assert.NotContains(t, string(content), bobAccount, name)
		assert.NotContains(t, string(content), "description_search", name)
	}

	// The owner is told, in their language.
	notice := mail.WaitForEmail(t, demo.Email, "Seus dados do Monthra foram exportados")
	assert.Contains(t, notice.Text, webOrigin+"/forgot-password")
	_, metrics := get(t, metricsURL)
	assert.Contains(t, metrics, `monthra_exports_total{dataset="account"} 1`)

	// Three tries an hour, failed step-ups included.
	status, _, body = postExport(t, native, api, map[string]string{"password": demo.Password}, asDemo)
	require.Equal(t, http.StatusOK, status, string(body))
	status, header, _ = postExport(t, native, api, map[string]string{"password": demo.Password}, asDemo)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, header.Get("Retry-After"))
	status, _, _ = postExport(t, native, api, map[string]string{"password": password}, asBob)
	assert.Equal(t, http.StatusOK, status, "the budget is by user")

	status, _, _ = postExport(t, native, api, map[string]string{"password": demo.Password}, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
}
