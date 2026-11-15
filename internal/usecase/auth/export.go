package auth

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/email"
)

const (
	// ExportFormat names the layout of the data export in its manifest.
	ExportFormat = "monthra-data-export"
	// ExportVersion is the version of that layout; it changes when a file
	// changes in a way its readers must know.
	ExportVersion = 1
	// ManifestFile describes the export: format, version, time and owner.
	ManifestFile = "manifest.json"
	// datasetAccount labels the exports of a whole account in the metrics.
	datasetAccount = "account"
)

// ExportDataInput is the password and, with MFA on, a current code.
type ExportDataInput struct {
	Password string
	Code     string
}

// DataExport is an export of the caller's account that passed the step-up,
// written by Write.
type DataExport struct {
	UserID string
	// At is when it was asked for, in UTC to the second: the time of the
	// manifest and of every file in the archive.
	At time.Time
	// Exporters write the files after the manifest, in order.
	Exporters []UserDataExporter
}

// manifest is the body of ManifestFile.
type manifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exportedAt"`
	UserID     string    `json:"userId"`
}

// ExportData starts an export of everything the caller's account holds
// (LGPD art. 18 II and V). Like DeleteAccount it asks for the password again,
// and for a code when MFA is on, so a stolen session alone cannot take the
// data away. Then it records data_exported in the audit log and emails the
// owner, with a reset link in case it was not them. The archive is written
// afterwards by DataExport.Write.
func (s *Service) ExportData(ctx context.Context, p core.Principal, in ExportDataInput, c Client) (DataExport, error) {
	u, err := s.currentUser(ctx, p)
	if err != nil {
		return DataExport{}, err
	}
	factor, err := s.stepUp(ctx, u, in.Password, in.Code, c)
	if err != nil {
		return DataExport{}, err
	}
	s.record(ctx, audit.EventDataExported, u.ID, p.SessionID, c, factor)
	s.notify(ctx, u, email.TemplateDataExported)
	s.Metrics.Exported(datasetAccount)
	return DataExport{UserID: u.ID, At: s.o.Now().UTC().Truncate(time.Second), Exporters: s.Exporters}, nil
}

// Write writes the export to w as a ZIP archive: the manifest, then the
// files of each exporter in order, read with ctx. Nothing is buffered
// beyond the compressor, so an export of any size holds little memory. A
// failed read returns its error with the archive left unfinished, so a
// reader never takes a short export for a whole one.
func (e DataExport) Write(ctx context.Context, w io.Writer) error {
	zw := zip.NewWriter(w)
	a := &archive{zw: zw, at: e.At, names: map[string]bool{}}
	mw, err := a.Create(ManifestFile)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(mw).Encode(manifest{Format: ExportFormat, Version: ExportVersion, ExportedAt: e.At, UserID: e.UserID}); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	for _, x := range e.Exporters {
		if err := x.ExportUserData(ctx, e.UserID, a); err != nil {
			return fmt.Errorf("export user data: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	return nil
}

// archive adds files to a ZIP at the root, compressed and dated at, each
// name once.
type archive struct {
	zw    *zip.Writer
	at    time.Time
	names map[string]bool
}

func (a *archive) Create(name string) (io.Writer, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || a.names[name] {
		return nil, fmt.Errorf("export file name %q: empty, nested or taken", name)
	}
	a.names[name] = true
	w, err := a.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: a.at})
	if err != nil {
		return nil, fmt.Errorf("add %s: %w", name, err)
	}
	return w, nil
}
