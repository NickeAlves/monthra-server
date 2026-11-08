package dto

import (
	"net/url"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// CreateRecurrenceRequest is the body of POST /recurrences: the fields of
// a transaction, without its day, and the rule. frequency is daily,
// weekly, monthly or yearly; interval (1 to 99, default 1) repeats every
// that many; startDate ("YYYY-MM-DD") is the first occurrence; endDate the
// last day one may fall on, or count (1 to 1000) the number of
// occurrences instead. The domain checks every field.
type CreateRecurrenceRequest struct {
	Kind        string   `json:"kind"`
	AccountID   string   `json:"accountId"`
	CategoryID  string   `json:"categoryId"`
	ToAccountID string   `json:"toAccountId"`
	Amount      int64    `json:"amount"`
	ToAmount    *int64   `json:"toAmount"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Frequency   string   `json:"frequency"`
	Interval    int      `json:"interval"`
	StartDate   string   `json:"startDate"`
	EndDate     string   `json:"endDate"`
	Count       int      `json:"count"`
}

// Input maps the request to the domain input.
func (r CreateRecurrenceRequest) Input() domain.RecurrenceInput {
	return domain.RecurrenceInput{
		Kind: r.Kind, AccountID: r.AccountID, CategoryID: r.CategoryID, ToAccountID: r.ToAccountID,
		Amount: r.Amount, ToAmount: r.ToAmount, Description: r.Description, Tags: r.Tags,
		Frequency: r.Frequency, Interval: r.Interval, StartDate: r.StartDate, EndDate: r.EndDate, Count: r.Count,
	}
}

// UpdateRecurrenceRequest is the body of PATCH /recurrences/{id}; absent
// (or null) fields stay as they are, and an empty endDate removes the end.
// It changes the occurrences not created yet only. The kind never changes.
type UpdateRecurrenceRequest struct {
	AccountID   *string   `json:"accountId"`
	CategoryID  *string   `json:"categoryId"`
	ToAccountID *string   `json:"toAccountId"`
	Amount      *int64    `json:"amount"`
	ToAmount    *int64    `json:"toAmount"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
	Frequency   *string   `json:"frequency"`
	Interval    *int      `json:"interval"`
	StartDate   *string   `json:"startDate"`
	EndDate     *string   `json:"endDate"`
}

// Patch maps the request to the domain patch.
func (r UpdateRecurrenceRequest) Patch() domain.RecurrencePatch {
	return domain.RecurrencePatch{
		AccountID: r.AccountID, CategoryID: r.CategoryID, ToAccountID: r.ToAccountID, Amount: r.Amount,
		ToAmount: r.ToAmount, Description: r.Description, Tags: r.Tags, Frequency: r.Frequency,
		Interval: r.Interval, StartDate: r.StartDate, EndDate: r.EndDate,
	}
}

// Recurrence is the public view of a recurrence.
type Recurrence struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	AccountID string `json:"accountId"`
	Currency  string `json:"currency"`
	// CategoryID is null on transfers; the to* fields are null except on
	// transfers.
	CategoryID  *string  `json:"categoryId"`
	ToAccountID *string  `json:"toAccountId"`
	ToCurrency  *string  `json:"toCurrency"`
	Amount      int64    `json:"amount"`
	ToAmount    *int64   `json:"toAmount"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Frequency   string   `json:"frequency"`
	Interval    int      `json:"interval"`
	StartDate   string   `json:"startDate"`
	// EndDate is null with no end; NextDate (the next occurrence not
	// created yet) once the recurrence ended; LastDate (the last one
	// created) before the first one.
	EndDate   *string   `json:"endDate"`
	NextDate  *string   `json:"nextDate"`
	LastDate  *string   `json:"lastDate"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// optionalDate writes a day, or null for none.
func optionalDate(d domain.Date) *string {
	if d.IsZero() {
		return nil
	}
	s := d.String()
	return &s
}

// NewRecurrence maps a recurrence to its public view; the owner id stays out.
func NewRecurrence(r domain.Recurrence) Recurrence {
	out := Recurrence{
		ID: r.ID, Kind: string(r.Kind), AccountID: r.AccountID, Currency: r.Currency, Amount: r.Amount,
		Description: r.Description, Tags: r.Tags, Frequency: string(r.Rule.Frequency), Interval: r.Rule.Interval,
		StartDate: r.Rule.Start.String(), EndDate: optionalDate(r.Rule.End), NextDate: optionalDate(r.Next),
		LastDate: optionalDate(r.Last), Source: string(r.Source), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if r.CategoryID != "" {
		out.CategoryID = &r.CategoryID
	}
	if r.Kind == domain.TransactionTransfer {
		out.ToAccountID, out.ToCurrency, out.ToAmount = &r.ToAccountID, &r.ToCurrency, &r.ToAmount
	}
	return out
}

// RecurrencesResponse is the body of GET /recurrences.
type RecurrencesResponse struct {
	Recurrences []Recurrence `json:"recurrences"`
}

// NewRecurrences maps the list; an empty one is [], never null.
func NewRecurrences(list []domain.Recurrence) RecurrencesResponse {
	out := RecurrencesResponse{Recurrences: make([]Recurrence, len(list))}
	for i, r := range list {
		out.Recurrences[i] = NewRecurrence(r)
	}
	return out
}

// UpcomingOccurrence is an occurrence a recurrence will create on date:
// what the transaction will be, with the invoice of a leg on a credit card.
type UpcomingOccurrence struct {
	RecurrenceID   string   `json:"recurrenceId"`
	Date           string   `json:"date"`
	Kind           string   `json:"kind"`
	AccountID      string   `json:"accountId"`
	Currency       string   `json:"currency"`
	CategoryID     *string  `json:"categoryId"`
	ToAccountID    *string  `json:"toAccountId"`
	ToCurrency     *string  `json:"toCurrency"`
	Amount         int64    `json:"amount"`
	ToAmount       *int64   `json:"toAmount"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
	InvoiceMonth   *string  `json:"invoiceMonth"`
	ToInvoiceMonth *string  `json:"toInvoiceMonth"`
}

// UpcomingResponse is the body of GET /recurrences/upcoming.
type UpcomingResponse struct {
	Occurrences []UpcomingOccurrence `json:"occurrences"`
}

// NewUpcoming maps a projection, made of occurrences not stored yet.
func NewUpcoming(list []domain.Transaction) UpcomingResponse {
	out := UpcomingResponse{Occurrences: make([]UpcomingOccurrence, len(list))}
	for i, t := range list {
		tx := NewTransaction(t)
		out.Occurrences[i] = UpcomingOccurrence{
			RecurrenceID: t.Recurrence.ID, Date: tx.Date, Kind: tx.Kind, AccountID: tx.AccountID, Currency: tx.Currency,
			CategoryID: tx.CategoryID, ToAccountID: tx.ToAccountID, ToCurrency: tx.ToCurrency, Amount: tx.Amount,
			ToAmount: tx.ToAmount, Description: tx.Description, Tags: tx.Tags, InvoiceMonth: tx.InvoiceMonth,
			ToInvoiceMonth: tx.ToInvoiceMonth,
		}
	}
	return out
}

// ParseUpcomingQuery reads GET /recurrences/upcoming?to&asOf, or returns a
// *domain.ValidationError naming each invalid parameter; absent ones are
// zero.
func ParseUpcomingQuery(q url.Values) (to, asOf domain.Date, err error) {
	var v domain.Validation
	if raw := q.Get("to"); raw != "" {
		to, err = domain.ParseDate(raw)
		v.Check("to", err)
	}
	if raw := q.Get("asOf"); raw != "" {
		asOf, err = domain.ParseDate(raw)
		v.Check("asOf", err)
	}
	if err := v.Err(); err != nil {
		return domain.Date{}, domain.Date{}, err
	}
	return to, asOf, nil
}
