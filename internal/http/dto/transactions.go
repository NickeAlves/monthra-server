package dto

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// CreateTransactionRequest is the body of POST /transactions. Amounts are
// positive integers in minor units (cents) of each account's currency. The
// domain checks every field, so missing ones are reported as required
// there. invoiceMonth and toInvoiceMonth ("YYYY-MM") name the card invoice
// of a leg on a credit card; absent, the day picks it. installments (2 to
// 48, expenses only) splits amount, the total, into monthly installments.
type CreateTransactionRequest struct {
	Kind           string   `json:"kind"`
	AccountID      string   `json:"accountId"`
	CategoryID     string   `json:"categoryId"`
	ToAccountID    string   `json:"toAccountId"`
	Amount         int64    `json:"amount"`
	ToAmount       *int64   `json:"toAmount"`
	Date           string   `json:"date"`
	InvoiceMonth   string   `json:"invoiceMonth"`
	ToInvoiceMonth string   `json:"toInvoiceMonth"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
	Installments   int      `json:"installments"`
}

// Input maps the request to the domain input.
func (r CreateTransactionRequest) Input() domain.TransactionInput {
	return domain.TransactionInput{
		Kind: r.Kind, AccountID: r.AccountID, CategoryID: r.CategoryID, ToAccountID: r.ToAccountID,
		Amount: r.Amount, ToAmount: r.ToAmount, Date: r.Date, InvoiceMonth: r.InvoiceMonth,
		ToInvoiceMonth: r.ToInvoiceMonth, Description: r.Description, Tags: r.Tags,
		Installments: r.Installments,
	}
}

// UpdateTransactionRequest is the body of PATCH /transactions/{id}; absent
// (or null) fields stay as they are. The kind is not a field: it never
// changes. An empty invoiceMonth (or toInvoiceMonth) picks the invoice from
// the day again.
type UpdateTransactionRequest struct {
	AccountID      *string   `json:"accountId"`
	CategoryID     *string   `json:"categoryId"`
	ToAccountID    *string   `json:"toAccountId"`
	Amount         *int64    `json:"amount"`
	ToAmount       *int64    `json:"toAmount"`
	Date           *string   `json:"date"`
	InvoiceMonth   *string   `json:"invoiceMonth"`
	ToInvoiceMonth *string   `json:"toInvoiceMonth"`
	Description    *string   `json:"description"`
	Tags           *[]string `json:"tags"`
}

// Patch maps the request to the domain patch.
func (r UpdateTransactionRequest) Patch() domain.TransactionPatch {
	return domain.TransactionPatch{
		AccountID: r.AccountID, CategoryID: r.CategoryID, ToAccountID: r.ToAccountID,
		Amount: r.Amount, ToAmount: r.ToAmount, Date: r.Date, InvoiceMonth: r.InvoiceMonth,
		ToInvoiceMonth: r.ToInvoiceMonth, Description: r.Description, Tags: r.Tags,
	}
}

// Transaction is the public view of a transaction.
type Transaction struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	AccountID string `json:"accountId"`
	Currency  string `json:"currency"`
	// CategoryID is null on transfers.
	CategoryID *string `json:"categoryId"`
	// ToAccountID, ToCurrency and ToAmount are null except on transfers.
	ToAccountID *string `json:"toAccountId"`
	ToCurrency  *string `json:"toCurrency"`
	Amount      int64   `json:"amount"`
	ToAmount    *int64  `json:"toAmount"`
	// Date is the civil day, "YYYY-MM-DD".
	Date string `json:"date"`
	// InvoiceMonth ("YYYY-MM") is the card invoice of the account leg, and
	// ToInvoiceMonth the one a transfer pays; null off a credit card.
	InvoiceMonth   *string  `json:"invoiceMonth"`
	ToInvoiceMonth *string  `json:"toInvoiceMonth"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
	// Installment is null outside an installment purchase.
	Installment *Installment `json:"installment"`
	Source      string       `json:"source"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// Installment places a transaction in an installment purchase: number of
// count, the purchase being groupId.
type Installment struct {
	GroupID string `json:"groupId"`
	Number  int    `json:"number"`
	Count   int    `json:"count"`
}

// NewTransaction maps a transaction to its public view; the owner id stays out.
func NewTransaction(t domain.Transaction) Transaction {
	out := Transaction{
		ID: t.ID, Kind: string(t.Kind), AccountID: t.AccountID, Currency: t.Currency, Amount: t.Amount,
		Date: t.Date.String(), Description: t.Description, Tags: t.Tags, Source: string(t.Source),
		CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC(),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if t.CategoryID != "" {
		out.CategoryID = &t.CategoryID
	}
	if t.IsTransfer() {
		out.ToAccountID, out.ToCurrency, out.ToAmount = &t.ToAccountID, &t.ToCurrency, &t.ToAmount
	}
	out.InvoiceMonth, out.ToInvoiceMonth = optionalMonth(t.InvoiceMonth), optionalMonth(t.ToInvoiceMonth)
	if t.Installment != nil {
		out.Installment = &Installment{GroupID: t.Installment.GroupID, Number: t.Installment.Number, Count: t.Installment.Count}
	}
	return out
}

// optionalMonth writes a month, or null for none.
func optionalMonth(m domain.Month) *string {
	if m.IsZero() {
		return nil
	}
	s := m.String()
	return &s
}

// TransactionsResponse is the body of GET /transactions: one page, and the
// cursor of the next one (null on the last page).
type TransactionsResponse struct {
	Transactions []Transaction `json:"transactions"`
	NextCursor   *string       `json:"nextCursor"`
}

// NewTransactions maps a page; an empty page is [], never null.
func NewTransactions(list []domain.Transaction, next *domain.TransactionCursor) TransactionsResponse {
	out := TransactionsResponse{Transactions: make([]Transaction, len(list))}
	for i, t := range list {
		out.Transactions[i] = NewTransaction(t)
	}
	if next != nil {
		c := EncodeCursor(*next)
		out.NextCursor = &c
	}
	return out
}

// TransactionQuery is a parsed GET /transactions query.
type TransactionQuery struct {
	Filter domain.TransactionFilter
	After  *domain.TransactionCursor
	// Limit is 0 when the query names none.
	Limit int
}

// ParseTransactionQuery reads the filters, the page size and the cursor of
// GET /transactions, or returns a *domain.ValidationError naming each
// invalid parameter. Empty parameters are ignored.
func ParseTransactionQuery(q url.Values) (TransactionQuery, error) {
	var v domain.Validation
	var out TransactionQuery
	var err error
	if raw := q.Get("from"); raw != "" {
		out.Filter.From, err = domain.ParseDate(raw)
		v.Check("from", err)
	}
	if raw := q.Get("to"); raw != "" {
		out.Filter.To, err = domain.ParseDate(raw)
		v.Check("to", err)
	}
	if !out.Filter.From.IsZero() && !out.Filter.To.IsZero() && out.Filter.To.Before(out.Filter.From) {
		v.Add("to", domain.CodeInvalid)
	}
	out.Filter.AccountID = q.Get("accountId")
	out.Filter.CategoryID = q.Get("categoryId")
	if raw := q.Get("kind"); raw != "" {
		kind, ok := domain.ParseTransactionKind(raw)
		if !ok {
			v.Add("kind", domain.CodeInvalid)
		}
		out.Filter.Kind = kind
	}
	if raw := q.Get("tag"); raw != "" {
		out.Filter.Tag, err = domain.NormalizeTag(raw)
		v.Check("tag", err)
	}
	if raw := q.Get("q"); raw != "" {
		out.Filter.Search, err = domain.NormalizeSearch(raw)
		v.Check("q", err)
	}
	if raw := q.Get("invoice"); raw != "" {
		out.Filter.Invoice, err = domain.ParseMonth(raw)
		v.Check("invoice", err)
		// An invoice belongs to one card.
		if out.Filter.AccountID == "" {
			v.Add("accountId", domain.CodeRequired)
		}
	}
	out.Filter.InstallmentGroupID = q.Get("installmentGroupId")
	if raw := q.Get("limit"); raw != "" {
		out.Limit, err = strconv.Atoi(raw)
		v.Check("limit", err)
	}
	if raw := q.Get("cursor"); raw != "" {
		out.After, err = DecodeCursor(raw)
		v.Check("cursor", err)
	}
	if err := v.Err(); err != nil {
		return TransactionQuery{}, err
	}
	return out, nil
}

// cursorID is what an id in a cursor may look like; ids are opaque to
// clients, but never more than this.
var cursorID = regexp.MustCompile(`^[0-9A-Za-z]{1,64}$`)

// EncodeCursor writes the position where a page ended as an opaque string:
// base64url of "YYYY-MM-DD|<id>". Changing it only moves within the
// caller's own transactions.
func EncodeCursor(c domain.TransactionCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.Date.String() + "|" + c.ID))
}

// DecodeCursor reads a cursor of EncodeCursor strictly.
func DecodeCursor(s string) (*domain.TransactionCursor, error) {
	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "cursor", Code: domain.CodeInvalid}}}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, invalid
	}
	day, id, ok := strings.Cut(string(raw), "|")
	if !ok || !cursorID.MatchString(id) {
		return nil, invalid
	}
	date, err := domain.ParseDate(day)
	if err != nil {
		return nil, invalid
	}
	return &domain.TransactionCursor{Date: date, ID: id}, nil
}

// ParseEditScope reads the scope of an edit or a deletion (?scope=), "this"
// when absent, or returns a *domain.ValidationError.
func ParseEditScope(raw string) (domain.EditScope, error) {
	scope, err := domain.ParseEditScope(raw)
	if err != nil {
		var v domain.Validation
		v.Check("scope", err)
		return "", v.Err()
	}
	return scope, nil
}

// ParseAsOf reads the day balances are computed up to ("YYYY-MM-DD", the
// reader's local today); an empty value is the zero Date, which the use
// case takes as today in UTC.
func ParseAsOf(raw string) (domain.Date, error) {
	if raw == "" {
		return domain.Date{}, nil
	}
	d, err := domain.ParseDate(raw)
	if err != nil {
		var v domain.Validation
		v.Check("asOf", err)
		return domain.Date{}, v.Err()
	}
	return d, nil
}
