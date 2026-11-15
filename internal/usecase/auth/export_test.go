package auth

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/email"
)

// fileExporter writes one file per name, its body the user id, or fails.
type fileExporter struct {
	names []string
	err   error
}

func (f fileExporter) ExportUserData(_ context.Context, userID string, a Archive) error {
	for _, name := range f.names {
		w, err := a.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, `"`+userID+`"`); err != nil {
			return err
		}
	}
	return f.err
}

// unzip reads every file of an archive, by name, and the names in order.
func unzip(t *testing.T, b []byte) (map[string][]byte, []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	files := map[string][]byte{}
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
		assert.Equal(t, zip.Deflate, f.Method, "compressed")
		rc, err := f.Open()
		require.NoError(t, err)
		files[f.Name], err = io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
	}
	return files, names
}

func TestExportDataWritesEveryExporterInOrder(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	p := h.signIn(t)
	h.Exporters = []UserDataExporter{fileExporter{names: []string{"profile.json"}}, fileExporter{names: []string{"accounts.json", "budgets.json"}}}
	mails := len(h.mail.sent)

	exp, err := h.ExportData(ctx, p, ExportDataInput{Password: password}, client)
	require.NoError(t, err)
	assert.Equal(t, u.ID, exp.UserID)
	assert.Equal(t, h.clock.now().UTC().Truncate(time.Second), exp.At)

	entry := h.audit.entries[len(h.audit.entries)-1]
	assert.Equal(t, audit.Entry{Event: audit.EventDataExported, UserID: u.ID, SessionID: p.SessionID, IP: client.IP.String(), UserAgent: client.UserAgent}, entry,
		"recorded with no second factor")
	require.Len(t, h.mail.sent, mails+1)
	notice := h.mail.sent[mails]
	assert.Equal(t, email.TemplateDataExported, notice.Template)
	assert.Equal(t, "ana@example.com", notice.To)
	assert.Equal(t, "https://app.monthra.example/forgot-password", notice.Link, "a way out if it was not the owner")
	assert.Equal(t, map[string]int{"account": 1}, h.metrics.exports)

	var out bytes.Buffer
	require.NoError(t, exp.Write(ctx, &out))
	files, names := unzip(t, out.Bytes())
	assert.Equal(t, []string{ManifestFile, "profile.json", "accounts.json", "budgets.json"}, names)
	assert.JSONEq(t, `"`+u.ID+`"`, string(files["budgets.json"]))
	var m map[string]any
	require.NoError(t, json.Unmarshal(files[ManifestFile], &m))
	assert.Equal(t, map[string]any{
		"format": "monthra-data-export", "version": float64(1),
		"exportedAt": exp.At.Format(time.RFC3339), "userId": u.ID,
	}, m)
}

func TestExportDataAsksForThePassword(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	p := h.signIn(t)
	mails := len(h.mail.sent)

	_, err := h.ExportData(ctx, p, ExportDataInput{Password: "wrong password!"}, client)
	require.ErrorIs(t, err, ErrPasswordIncorrect)
	assert.Equal(t, 1, h.lockout.fails[anaKey], "a wrong password counts toward the lockout")
	assert.NotContains(t, h.audit.events(), audit.EventDataExported)
	assert.Len(t, h.mail.sent, mails, "no notice")
	assert.Empty(t, h.metrics.exports)

	_, err = h.ExportData(ctx, p, ExportDataInput{}, client)
	require.ErrorIs(t, err, ErrPasswordIncorrect)
}

func TestExportDataWithMFAAsksForACode(t *testing.T) {
	h := newHarness()
	_, codes := h.mfaUser(t)
	p := h.principal()

	_, err := h.ExportData(ctx, p, ExportDataInput{Password: password}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid, "a code is required")
	assert.Zero(t, h.lockout.fails[anaKey], "a missing code is not a guess")

	_, err = h.ExportData(ctx, p, ExportDataInput{Password: password, Code: "000000"}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid)
	assert.Equal(t, 1, h.lockout.fails[anaKey], "a wrong code counts toward the lockout")
	assert.Contains(t, h.audit.events(), audit.EventMFAFailure)
	assert.NotContains(t, h.audit.events(), audit.EventDataExported)

	_, err = h.ExportData(ctx, p, ExportDataInput{Password: password, Code: codes[0]}, client)
	require.NoError(t, err)
	entry := h.audit.entries[len(h.audit.entries)-1]
	assert.Equal(t, audit.EventDataExported, entry.Event)
	assert.Equal(t, factorRecoveryCode, entry.Reason, "records how the second factor was checked")

	_, err = h.ExportData(ctx, p, ExportDataInput{Password: password, Code: codes[0]}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid, "a recovery code works once")
	stored, err := h.users.ByID(ctx, p.UserID)
	require.NoError(t, err)
	assert.Len(t, stored.MFA.RecoveryCodes, len(codes)-1)
}

func TestExportDataOfADeletedAccount(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	p := h.signIn(t)
	require.NoError(t, h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client))
	_, err := h.ExportData(ctx, p, ExportDataInput{Password: password}, client)
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestDataExportWriteFails(t *testing.T) {
	down := errors.New("accounts down")
	cases := map[string]struct {
		exporters []UserDataExporter
		want      error
	}{
		"a failed read":        {exporters: []UserDataExporter{fileExporter{names: []string{"accounts.json"}, err: down}}, want: down},
		"a name taken":         {exporters: []UserDataExporter{fileExporter{names: []string{"a.json"}}, fileExporter{names: []string{"a.json"}}}},
		"the manifest's name":  {exporters: []UserDataExporter{fileExporter{names: []string{ManifestFile}}}},
		"a nested name":        {exporters: []UserDataExporter{fileExporter{names: []string{"../a.json"}}}},
		"an empty name":        {exporters: []UserDataExporter{fileExporter{names: []string{""}}}},
		"a backslash in names": {exporters: []UserDataExporter{fileExporter{names: []string{`a\b.json`}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exp := DataExport{UserID: "u1", At: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), Exporters: tc.exporters}
			var out bytes.Buffer
			err := exp.Write(ctx, &out)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			_, zerr := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			assert.Error(t, zerr, "an unfinished archive cannot be read as a whole one")
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestDataExportWriteToAGoneClient(t *testing.T) {
	exp := DataExport{UserID: "u1", At: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
		Exporters: []UserDataExporter{fileExporter{names: []string{"a.json"}}}}
	require.Error(t, exp.Write(ctx, failingWriter{}))
}
