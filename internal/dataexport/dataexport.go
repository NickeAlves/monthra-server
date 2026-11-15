// Package dataexport writes the files of the LGPD data export (POST
// /me/export): each exporter is an authuc.UserDataExporter that writes what
// one store holds of a user as JSON, in the API's shapes (http/dto), stored
// fields only: no balance, budget usage or position, which the records
// make again. Lists are arrays streamed one record per line, so a file of
// any size holds one record in memory at a time.
package dataexport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"

	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// File names of the export, besides authuc.ManifestFile.
const (
	FileProfile             = "profile.json"
	FileAccounts            = "accounts.json"
	FileCategories          = "categories.json"
	FileTransactions        = "transactions.json"
	FileRecurrences         = "recurrences.json"
	FileBudgets             = "budgets.json"
	FileInvestments         = "investments.json"
	FileInvestmentMovements = "investment-movements.json"
	FileSessions            = "sessions.json"
	FileAuditLog            = "audit-log.json"
)

// List is a file holding a JSON array of the records Records yields.
type List struct {
	Name    string
	Records func(ctx context.Context, userID string) iter.Seq2[any, error]
}

// ExportUserData writes the file of userID.
func (l List) ExportUserData(ctx context.Context, userID string, a authuc.Archive) error {
	w, err := a.Create(l.Name)
	if err != nil {
		return err
	}
	if err := writeArray(w, l.Records(ctx, userID)); err != nil {
		return fmt.Errorf("%s: %w", l.Name, err)
	}
	return nil
}

// Object is a file holding the one JSON object Read returns.
type Object struct {
	Name string
	Read func(ctx context.Context, userID string) (any, error)
}

// ExportUserData writes the file of userID.
func (o Object) ExportUserData(ctx context.Context, userID string, a authuc.Archive) error {
	v, err := o.Read(ctx, userID)
	if err != nil {
		return fmt.Errorf("%s: %w", o.Name, err)
	}
	w, err := a.Create(o.Name)
	if err != nil {
		return err
	}
	b, err := encode(v)
	if err != nil {
		return fmt.Errorf("%s: %w", o.Name, err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%s: %w", o.Name, err)
	}
	return nil
}

// writeArray writes the values of seq to w as a JSON array, one value per
// line ("[]" when there is none). A failed read returns its error, the
// array left unclosed.
func writeArray(w io.Writer, seq iter.Seq2[any, error]) error {
	first := true
	for v, err := range seq {
		if err != nil {
			return err
		}
		b, err := encode(v)
		if err != nil {
			return err
		}
		prefix := ",\n"
		if first {
			prefix, first = "[\n", false
		}
		if _, err := io.WriteString(w, prefix); err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	end := "\n]\n"
	if first {
		end = "[]\n"
	}
	_, err := io.WriteString(w, end)
	return err
}

// encode marshals v as the API does, but keeps <, > and & as they are:
// the file is read as data, never embedded in HTML.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode record: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// each maps the values of seq with f.
func each[T any](seq iter.Seq2[T, error], f func(T) any) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		for v, err := range seq {
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(f(v), nil) {
				return
			}
		}
	}
}

// all yields list mapped with f, or err.
func all[T any](list []T, err error, f func(T) any) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		if err != nil {
			yield(nil, err)
			return
		}
		for _, v := range list {
			if !yield(f(v), nil) {
				return
			}
		}
	}
}
