package dataexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

var (
	ctx    = context.Background()
	at     = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	errOff = errors.New("store down")
)

const owner = "64b000000000000000000001"

// files is an archive in memory.
type files map[string]*bytes.Buffer

func (f files) Create(name string) (io.Writer, error) {
	if _, ok := f[name]; ok {
		return nil, fmt.Errorf("%s twice", name)
	}
	f[name] = &bytes.Buffer{}
	return f[name], nil
}

var _ authuc.Archive = files{}

// export runs x for owner and decodes its one file into v.
func export(t *testing.T, x authuc.UserDataExporter, name string, v any) {
	t.Helper()
	a := files{}
	require.NoError(t, x.ExportUserData(ctx, owner, a))
	require.Contains(t, a, name)
	require.NoError(t, json.Unmarshal(a[name].Bytes(), v), a[name].String())
}

func seqOf[T any](list ...T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, v := range list {
			if !yield(v, nil) {
				return
			}
		}
	}
}

func TestWriteArray(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeArray(&out, seqOf[any]()))
	assert.Equal(t, "[]\n", out.String())

	out.Reset()
	require.NoError(t, writeArray(&out, seqOf[any](map[string]string{"a": "R&D <1>"}, map[string]int{"b": 2})))
	assert.Equal(t, "[\n{\"a\":\"R&D <1>\"},\n{\"b\":2}\n]\n", out.String(), "one record per line, nothing HTML-escaped")

	out.Reset()
	failing := func(yield func(any, error) bool) {
		if yield(1, nil) {
			yield(nil, errOff)
		}
	}
	require.ErrorIs(t, writeArray(&out, failing), errOff)
	assert.False(t, json.Valid(out.Bytes()), "a failed read leaves the array unclosed")

	require.Error(t, writeArray(&out, seqOf[any](func() {})), "a value JSON cannot hold")
}

type fakeUsers struct{ err error }

func (f fakeUsers) ByID(_ context.Context, id string) (domain.User, error) {
	verified := at
	return domain.User{
		ID: id, Name: "Ana", Email: "ana@example.com", PasswordHash: "$argon2id$secret-hash",
		EmailVerifiedAt: &verified, Preferences: domain.Preferences{Locale: "pt-BR", Theme: "dark", Currency: "BRL"},
		MFA: domain.MFA{Secret: "sealed-secret", RecoveryCodes: []string{"code-hash"}}, CreatedAt: at,
	}, f.err
}

func TestProfile(t *testing.T) {
	a := files{}
	require.NoError(t, Profile(fakeUsers{}).ExportUserData(ctx, owner, a))
	body := a[FileProfile].String()
	assert.NotContains(t, body, "secret", "no hash or MFA secret")
	assert.NotContains(t, body, "code-hash")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, owner, got["id"])
	assert.Equal(t, "ana@example.com", got["email"])
	assert.Contains(t, got, "preferences")

	require.ErrorIs(t, Profile(fakeUsers{err: errOff}).ExportUserData(ctx, owner, files{}), errOff)
	assert.Error(t, Profile(fakeUsers{}).ExportUserData(ctx, owner, files{FileProfile: nil}), "a name taken")
}

type fakeAccounts struct {
	archived bool
	err      error
}

func (f *fakeAccounts) List(_ context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	f.archived = includeArchived
	return []domain.Account{{ID: "a1", UserID: userID, Name: "Card", Type: domain.AccountCreditCard, Currency: "BRL",
		Card: &domain.CardSettings{ClosingDay: 3, DueDay: 10}, ArchivedAt: &at, CreatedAt: at, UpdatedAt: at}}, f.err
}

func TestAccountsFile(t *testing.T) {
	accounts := &fakeAccounts{}
	var got []map[string]any
	export(t, AccountsFile(accounts), FileAccounts, &got)
	assert.True(t, accounts.archived, "the archived ones too")
	require.Len(t, got, 1)
	assert.Equal(t, true, got[0]["archived"])
	assert.NotContains(t, got[0], "balance", "derived figures stay out")
	assert.NotContains(t, got[0], "projectedBalance")
	assert.NotContains(t, got[0], "userId")
	assert.Equal(t, map[string]any{"closingDay": float64(3), "dueDay": float64(10), "limit": nil}, got[0]["card"])

	require.ErrorIs(t, AccountsFile(&fakeAccounts{err: errOff}).ExportUserData(ctx, owner, files{}), errOff)
}

type fakeCategories struct{ kind domain.CategoryKind }

func (f *fakeCategories) List(_ context.Context, _ string, kind domain.CategoryKind) ([]domain.Category, error) {
	f.kind = kind
	return []domain.Category{{ID: "c1", DefaultKey: "food", Kind: domain.CategoryExpense, Icon: "utensils", Color: "#1E6B52"}}, nil
}

func TestCategoriesFile(t *testing.T) {
	categories := &fakeCategories{kind: "unset"}
	a := files{}
	require.NoError(t, CategoriesFile(categories).ExportUserData(i18n.WithLocale(ctx, i18n.EnUS), owner, a))
	assert.Empty(t, categories.kind, "every kind")
	var got []map[string]any
	require.NoError(t, json.Unmarshal(a[FileCategories].Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "food", got[0]["defaultKey"])
	assert.Equal(t, domain.Category{DefaultKey: "food"}.DisplayName(i18n.EnUS), got[0]["name"], "named in the stored locale")
}

type fakeLedger struct {
	list  []domain.Transaction
	pages int
	err   error
}

func (f *fakeLedger) List(_ context.Context, _ string, filter domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error) {
	if filter != (domain.TransactionFilter{}) {
		return nil, errors.New("filtered")
	}
	f.pages++
	if f.err != nil && f.pages == 2 {
		return nil, f.err
	}
	start := 0
	if after != nil {
		start = slices.IndexFunc(f.list, func(t domain.Transaction) bool { return t.ID == after.ID }) + 1
	}
	return f.list[start:min(start+limit, len(f.list))], nil
}

type fakeMaterializer struct{ calls []string }

func (f *fakeMaterializer) Materialize(_ context.Context, userID string, asOf domain.Date) {
	f.calls = append(f.calls, userID+"@"+asOf.String())
}

func TestTransactionsFilePagesThroughEveryTransaction(t *testing.T) {
	day, err := domain.ParseDate("2026-11-15")
	require.NoError(t, err)
	ledger := &fakeLedger{}
	for i := range 2*TransactionsPageSize + 1 {
		ledger.list = append(ledger.list, domain.Transaction{ID: fmt.Sprintf("t%04d", i), Kind: domain.TransactionExpense, Date: day.AddDays(-i)})
	}
	recurrences := &fakeMaterializer{}
	var got []map[string]any
	export(t, TransactionsFile(ledger, recurrences), FileTransactions, &got)
	assert.Equal(t, []string{owner + "@" + domain.Date{}.String()}, recurrences.calls, "the occurrences due are created first, as of UTC today")
	assert.Equal(t, 3, ledger.pages)
	require.Len(t, got, len(ledger.list))
	assert.Equal(t, "t0000", got[0]["id"])
	assert.Equal(t, "t1000", got[1000]["id"])
	assert.NotContains(t, got[0], "userId")

	ledger.pages, ledger.list = 0, ledger.list[:TransactionsPageSize]
	export(t, TransactionsFile(ledger, recurrences), FileTransactions, &got)
	assert.Equal(t, 2, ledger.pages, "a full page asks for the next one")

	ledger.pages, ledger.err = 0, errOff
	require.ErrorIs(t, TransactionsFile(ledger, recurrences).ExportUserData(ctx, owner, files{}), errOff)
}

type fakeRecurrences struct{}

func (fakeRecurrences) List(context.Context, string) ([]domain.Recurrence, error) {
	return []domain.Recurrence{{ID: "r1", Kind: domain.TransactionIncome, Rule: domain.RecurrenceRule{Frequency: domain.FrequencyMonthly, Interval: 1}}}, nil
}

type fakeBudgets struct{}

func (fakeBudgets) All(_ context.Context, userID string) iter.Seq2[domain.Budget, error] {
	return seqOf(domain.Budget{ID: "b1", UserID: userID, CategoryID: "c1", Month: domain.NewMonth(2026, time.October), Currency: "BRL",
		Limit: 1000, WarnAt: 80, WarnedAt: &at, CreatedAt: at, UpdatedAt: at})
}

func TestRecordFiles(t *testing.T) {
	var recurrences []map[string]any
	export(t, RecurrencesFile(fakeRecurrences{}), FileRecurrences, &recurrences)
	require.Len(t, recurrences, 1)
	assert.Equal(t, "r1", recurrences[0]["id"])

	var budgets []map[string]any
	export(t, BudgetsFile(fakeBudgets{}), FileBudgets, &budgets)
	assert.Equal(t, []map[string]any{{
		"id": "b1", "categoryId": "c1", "month": "2026-10", "currency": "BRL", "limit": float64(1000), "warnAt": float64(80),
		"createdAt": at.Format(time.RFC3339), "updatedAt": at.Format(time.RFC3339),
	}}, budgets, "no usage, status or alert marks")
}

type fakeInvestments struct{ err error }

func (f fakeInvestments) List(context.Context, string) ([]domain.Investment, error) {
	return []domain.Investment{
		{ID: "i1", Name: "Fund", Type: domain.InvestmentFund, Mode: domain.ModeValue, Currency: "BRL", CreatedAt: at},
		{ID: "i2", Name: "Stock", Type: domain.InvestmentStock, Mode: domain.ModeUnits, Currency: "USD", Ticker: "ACME", AccountID: "a1"},
	}, f.err
}

type fakeMovements struct{ err error }

func (f fakeMovements) Of(_ context.Context, _, investmentID string) ([]domain.Movement, error) {
	day, _ := domain.ParseDate("2026-11-01")
	return []domain.Movement{
		{ID: investmentID + "-m2", InvestmentID: investmentID, Type: domain.MovementFee, Mode: domain.ModeValue, Date: day, Amount: 5},
		{ID: investmentID + "-m1", InvestmentID: investmentID, Type: domain.MovementFee, Mode: domain.ModeValue, Date: day, Amount: 3},
	}, f.err
}

func TestInvestmentFiles(t *testing.T) {
	var investments []map[string]any
	export(t, InvestmentsFile(fakeInvestments{}), FileInvestments, &investments)
	require.Len(t, investments, 2)
	assert.NotContains(t, investments[0], "position", "derived figures stay out")
	assert.Nil(t, investments[0]["ticker"])
	assert.Equal(t, "ACME", investments[1]["ticker"])
	assert.Equal(t, "a1", investments[1]["accountId"])

	var movements []map[string]any
	export(t, MovementsFile(fakeInvestments{}, fakeMovements{}), FileInvestmentMovements, &movements)
	ids := make([]any, len(movements))
	for i, m := range movements {
		ids[i] = m["id"]
	}
	assert.Equal(t, []any{"i1-m2", "i1-m1", "i2-m2", "i2-m1"}, ids, "by investment, each one's as listed")

	require.ErrorIs(t, MovementsFile(fakeInvestments{err: errOff}, fakeMovements{}).ExportUserData(ctx, owner, files{}), errOff)
	require.ErrorIs(t, MovementsFile(fakeInvestments{}, fakeMovements{err: errOff}).ExportUserData(ctx, owner, files{}), errOff)
	require.ErrorIs(t, InvestmentsFile(fakeInvestments{err: errOff}).ExportUserData(ctx, owner, files{}), errOff)
}

type fakeSessions struct{}

func (fakeSessions) ForUser(_ context.Context, userID string) iter.Seq2[domain.Session, error] {
	revoked := at.Add(time.Hour)
	return seqOf(
		domain.Session{ID: "s2", UserID: userID, UserAgent: "ua", IP: "198.51.100.7", CreatedAt: at, LastUsedAt: at, ExpiresAt: at, RevokedAt: &revoked},
		domain.Session{ID: "s1", UserID: userID, CreatedAt: at, LastUsedAt: at, ExpiresAt: at},
	)
}

type fakeAuditLog struct{ err error }

func (f fakeAuditLog) ForUser(_ context.Context, userID string) iter.Seq2[audit.Entry, error] {
	return func(yield func(audit.Entry, error) bool) {
		if !yield(audit.Entry{Event: audit.EventLoginSuccess, UserID: userID, SessionID: "s1", IP: "198.51.100.7", UserAgent: "ua", CreatedAt: at}, nil) {
			return
		}
		if f.err != nil {
			yield(audit.Entry{}, f.err)
			return
		}
		yield(audit.Entry{Event: audit.EventReauthFailure, UserID: userID, Reason: "wrong_password", CreatedAt: at}, nil)
	}
}

func TestSecurityFiles(t *testing.T) {
	var sessions []map[string]any
	export(t, SessionsFile(fakeSessions{}), FileSessions, &sessions)
	require.Len(t, sessions, 2)
	assert.Equal(t, at.Add(time.Hour).Format(time.RFC3339), sessions[0]["revokedAt"])
	assert.Nil(t, sessions[1]["revokedAt"])
	assert.NotContains(t, sessions[0], "current")

	var entries []map[string]any
	export(t, AuditLogFile(fakeAuditLog{}), FileAuditLog, &entries)
	assert.Equal(t, []map[string]any{
		{"event": "login_success", "sessionId": "s1", "ip": "198.51.100.7", "userAgent": "ua", "reason": nil, "createdAt": at.Format(time.RFC3339)},
		{"event": "reauth_failure", "sessionId": nil, "ip": nil, "userAgent": nil, "reason": "wrong_password", "createdAt": at.Format(time.RFC3339)},
	}, entries, "no user id")

	require.ErrorIs(t, AuditLogFile(fakeAuditLog{err: errOff}).ExportUserData(ctx, owner, files{}), errOff)
	assert.Error(t, AuditLogFile(fakeAuditLog{}).ExportUserData(ctx, owner, files{FileAuditLog: nil}), "a name taken")
}
