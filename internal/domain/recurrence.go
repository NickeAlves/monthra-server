package domain

import (
	"errors"
	"slices"
	"time"
)

// Bounds of a recurrence rule.
const (
	MaxRecurrenceInterval = 99
	// MaxRecurrenceCount bounds the occurrences a rule may be given by
	// count (it ends on the date of the last one).
	MaxRecurrenceCount = 1000
)

// Frequency is the unit a recurrence repeats by.
type Frequency string

// Supported frequencies.
const (
	FrequencyDaily   Frequency = "daily"
	FrequencyWeekly  Frequency = "weekly"
	FrequencyMonthly Frequency = "monthly"
	FrequencyYearly  Frequency = "yearly"
)

// Frequencies lists the supported frequencies.
var Frequencies = []Frequency{FrequencyDaily, FrequencyWeekly, FrequencyMonthly, FrequencyYearly}

// ParseFrequency returns the frequency named s, if it is supported.
func ParseFrequency(s string) (Frequency, bool) {
	for _, f := range Frequencies {
		if string(f) == s {
			return f, true
		}
	}
	return "", false
}

// RecurrenceRule is a simplified RRULE: every Interval days, weeks, months
// or years from Start, through End (included) when it is set. Occurrences
// count from Start, so a monthly rule from January 31 falls on February 28
// and on March 31 again, and a yearly one from February 29 on February 28
// of common years.
type RecurrenceRule struct {
	Frequency Frequency
	Interval  int
	Start     Date
	// End is the last day an occurrence may fall on; zero is no end.
	End Date
}

// Nth returns occurrence k of the rule, from 0 (Start), ignoring End.
func (r RecurrenceRule) Nth(k int) Date {
	n := k * r.Interval
	switch r.Frequency {
	case FrequencyDaily:
		return r.Start.AddDays(n)
	case FrequencyWeekly:
		return r.Start.AddDays(7 * n)
	case FrequencyMonthly:
		return r.Start.AddMonths(n)
	default:
		return r.Start.AddMonths(12 * n)
	}
}

// NextOn returns the first occurrence on or after day d, and false when the
// rule has none left: after End, or after MaxDate.
func (r RecurrenceRule) NextOn(d Date) (Date, bool) {
	k := 0
	if r.Start.Before(d) {
		switch r.Frequency {
		case FrequencyDaily, FrequencyWeekly:
			step := r.Interval
			if r.Frequency == FrequencyWeekly {
				step *= 7
			}
			k = (d.daysSince(r.Start) + step - 1) / step
		default:
			step := r.Interval
			if r.Frequency == FrequencyYearly {
				step *= 12
			}
			// Occurrence k falls in month Start + k*step; the one in d's month
			// (or the last before it) may still come before d.
			k = MonthOf(d).Sub(MonthOf(r.Start)) / step
			if r.Nth(k).Before(d) {
				k++
			}
		}
	}
	next := r.Nth(k)
	if MaxDate.Before(next) || (!r.End.IsZero() && r.End.Before(next)) {
		return Date{}, false
	}
	return next, true
}

// RecurrenceLink places a transaction among the occurrences of a
// recurrence. Date is the day the rule gave it, which stays when the
// transaction's own day is edited, so the occurrence is never created
// again. It never changes once written.
type RecurrenceLink struct {
	ID   string
	Date Date
}

// Recurrence is a transaction the owner repeats (rent, a subscription, a
// salary, a monthly saving) and the rule it repeats by. Its occurrences are
// created as transactions when they fall due; the recurrence holds what the
// next ones will be.
type Recurrence struct {
	ID     string
	UserID string
	// The template of the occurrences, with the rules of a Transaction.
	Kind        TransactionKind
	AccountID   string
	Currency    string
	CategoryID  string
	ToAccountID string
	ToCurrency  string
	Amount      int64
	ToAmount    int64
	Description string
	Tags        []string
	Rule        RecurrenceRule
	// Next is the next occurrence not created yet; zero once the rule has
	// none left (the recurrence ended).
	Next Date
	// Last is the last occurrence created; zero before the first one.
	Last      Date
	Source    Source
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Active reports whether the recurrence still has occurrences to create.
func (r Recurrence) Active() bool { return !r.Next.IsZero() }

// template is the transaction the recurrence creates on day, with no
// invoice and no link.
func (r Recurrence) template(day Date) Transaction {
	return Transaction{
		UserID: r.UserID, Kind: r.Kind, AccountID: r.AccountID, Currency: r.Currency,
		CategoryID: r.CategoryID, Amount: r.Amount, ToAccountID: r.ToAccountID, ToCurrency: r.ToCurrency,
		ToAmount: r.ToAmount, Date: day, Description: r.Description, Tags: slices.Clone(r.Tags), Source: r.Source,
	}
}

// setTemplate copies the template fields of t.
func (r *Recurrence) setTemplate(t Transaction) {
	r.AccountID, r.Currency, r.CategoryID, r.Amount = t.AccountID, t.Currency, t.CategoryID, t.Amount
	r.ToAccountID, r.ToCurrency, r.ToAmount = t.ToAccountID, t.ToCurrency, t.ToAmount
	r.Description, r.Tags = t.Description, slices.Clone(t.Tags)
}

// equal reports whether r and o hold the same editable values.
func (r Recurrence) equal(o Recurrence) bool {
	return r.template(Date{}).equal(o.template(Date{})) && r.Rule.equal(o.Rule) && r.Next.Equal(o.Next)
}

// equal reports whether r and o are the same rule.
func (r RecurrenceRule) equal(o RecurrenceRule) bool {
	return r.Frequency == o.Frequency && r.Interval == o.Interval && r.Start.Equal(o.Start) && r.End.Equal(o.End)
}

// RecurrenceInput is a new recurrence as the owner describes it: the
// fields of a transaction, without its day, and the rule.
type RecurrenceInput struct {
	Kind        string
	AccountID   string
	CategoryID  string
	ToAccountID string
	Amount      int64
	ToAmount    *int64
	Description string
	Tags        []string
	Frequency   string
	// Interval is 1 to MaxRecurrenceInterval; zero is 1.
	Interval  int
	StartDate string
	// EndDate ("YYYY-MM-DD") is the last day an occurrence may fall on, or
	// empty for no end. Count (1 to MaxRecurrenceCount) ends the rule on
	// its Count-th occurrence instead; it is stored as that EndDate.
	EndDate string
	Count   int
}

// NewRecurrence validates in against the records it names and returns the
// manual recurrence of userID, whose first occurrence falls on its start,
// or a *ValidationError naming every invalid field. The template follows
// the rules of NewTransaction on the start day (an archived account takes
// no new recurrence either).
func NewRecurrence(userID string, in RecurrenceInput, refs TransactionRefs, now time.Time) (Recurrence, error) {
	var v Validation
	t, err := NewTransaction(userID, TransactionInput{
		Kind: in.Kind, AccountID: in.AccountID, CategoryID: in.CategoryID, ToAccountID: in.ToAccountID,
		Amount: in.Amount, ToAmount: in.ToAmount, Date: in.StartDate, Description: in.Description, Tags: in.Tags,
	}, refs, now)
	addRenamed(&v, err, "date", "startDate")
	start, _ := ParseDate(in.StartDate)

	rule := RecurrenceRule{Start: start, Interval: in.Interval}
	var ok bool
	rule.Frequency, ok = ParseFrequency(in.Frequency)
	switch {
	case in.Frequency == "":
		v.Add("frequency", CodeRequired)
	case !ok:
		v.Add("frequency", CodeInvalid)
	}
	if rule.Interval == 0 {
		rule.Interval = 1
	}
	checkInterval(&v, rule.Interval)
	if in.EndDate != "" {
		rule.End, err = ParseDate(in.EndDate)
		v.Check("endDate", err)
	}
	if in.Count != 0 {
		switch {
		case in.EndDate != "":
			v.Add("count", CodeInvalid)
		case in.Count < 1 || in.Count > MaxRecurrenceCount:
			v.Add("count", CodeOutOfRange)
		case ok && !start.IsZero() && rule.Interval >= 1 && rule.Interval <= MaxRecurrenceInterval:
			rule.End = rule.Nth(in.Count - 1)
			if MaxDate.Before(rule.End) {
				v.Add("count", CodeOutOfRange)
			}
		}
	}
	checkEnd(&v, rule)
	if err := v.Err(); err != nil {
		return Recurrence{}, err
	}

	now = now.UTC()
	r := Recurrence{UserID: userID, Kind: t.Kind, Rule: rule, Next: start, Source: SourceManual, CreatedAt: now, UpdatedAt: now}
	r.setTemplate(t)
	return r, nil
}

// addRenamed records the field errors of err (a *ValidationError, or nil),
// naming field from as to.
func addRenamed(v *Validation, err error, from, to string) {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return
	}
	for _, f := range ve.Fields {
		if f.Field == from {
			f.Field = to
		}
		v.Add(f.Field, f.Code)
	}
}

func checkInterval(v *Validation, n int) {
	if n < 1 || n > MaxRecurrenceInterval {
		v.Add("interval", CodeOutOfRange)
	}
}

// checkEnd refuses an end before the start of a rule.
func checkEnd(v *Validation, r RecurrenceRule) {
	if !r.Start.IsZero() && !r.End.IsZero() && r.End.Before(r.Start) {
		v.Add("endDate", CodeInvalid)
	}
}

// RecurrencePatch changes some fields of a recurrence; nil fields stay as
// they are. The kind never changes. An empty EndDate removes the end.
//
// It changes what the next occurrences will be, never the transactions
// already created (a scoped edit of a transaction changes those).
type RecurrencePatch struct {
	AccountID   *string
	CategoryID  *string
	ToAccountID *string
	Amount      *int64
	ToAmount    *int64
	Description *string
	Tags        *[]string
	Frequency   *string
	Interval    *int
	StartDate   *string
	EndDate     *string
}

// Apply checks the patch and returns current with the changes, whether
// anything changed, or a *ValidationError naming each invalid field. The
// template takes the rules of TransactionPatch.Apply: refs holds the
// records the patch moves it to, and may hold its unchanged accounts too.
//
// Only what changed is checked, so a client that sends its whole form is
// not refused for the fields it left as they were. A new start must come
// after the last occurrence created (a rule never covers again the days it
// already gave), and the next occurrence is the first of the new rule after
// that one. A recurrence brought back to life (by a later end) must not be
// on an archived account; refs holds its accounts then.
func (p RecurrencePatch) Apply(current Recurrence, refs TransactionRefs, now time.Time) (Recurrence, bool, error) {
	var v Validation
	day := current.Next
	if day.IsZero() {
		day = current.Rule.Start
	}
	next := current
	next.Tags = slices.Clone(current.Tags)
	t, _, err := TransactionPatch{
		AccountID: p.AccountID, CategoryID: p.CategoryID, ToAccountID: p.ToAccountID, Amount: p.Amount,
		ToAmount: p.ToAmount, Description: p.Description, Tags: p.Tags,
	}.Apply(current.template(day), refs, now)
	addRenamed(&v, err, "", "")
	if err == nil {
		next.setTemplate(t)
	}

	rule := current.Rule
	if p.Frequency != nil && *p.Frequency != string(rule.Frequency) {
		f, ok := ParseFrequency(*p.Frequency)
		if !ok {
			v.Add("frequency", CodeInvalid)
		}
		rule.Frequency = f
	}
	if p.Interval != nil && *p.Interval != rule.Interval {
		checkInterval(&v, *p.Interval)
		rule.Interval = *p.Interval
	}
	if p.StartDate != nil && *p.StartDate != rule.Start.String() {
		start, err := ParseDate(*p.StartDate)
		v.Check("startDate", err)
		if err == nil && !current.Last.IsZero() && !current.Last.Before(start) {
			v.Add("startDate", CodeOutOfRange)
		}
		rule.Start = start
	}
	if p.EndDate != nil && *p.EndDate != dateString(rule.End) {
		rule.End = Date{}
		if *p.EndDate != "" {
			rule.End, err = ParseDate(*p.EndDate)
			v.Check("endDate", err)
		}
	}
	checkEnd(&v, rule)
	if err := v.Err(); err != nil {
		return Recurrence{}, false, err
	}

	if !rule.equal(current.Rule) {
		next.Rule = rule
		from := rule.Start
		if !current.Last.IsZero() && from.Before(current.Last.AddDays(1)) {
			from = current.Last.AddDays(1)
		}
		next.Next, _ = rule.NextOn(from)
	}
	if next.Active() && !current.Active() {
		if archivedRef(refs.Account, next.AccountID) {
			v.Add("accountId", CodeArchived)
		}
		if next.Kind == TransactionTransfer && archivedRef(refs.ToAccount, next.ToAccountID) {
			v.Add("toAccountId", CodeArchived)
		}
		if err := v.Err(); err != nil {
			return Recurrence{}, false, err
		}
	}
	if next.equal(current) {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

// archivedRef reports whether acc, found for account id, is archived.
func archivedRef(acc *Account, id string) bool {
	return acc != nil && acc.ID == id && acc.Archived()
}

// dateString writes a day, or "" for none.
func dateString(d Date) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

// Occurrences returns the transactions r creates from its next occurrence
// through day through (included), at most max of them, and the occurrence
// that comes after them (zero when the rule has none left). refs holds the
// accounts of r: on a credit card, each occurrence goes to the invoice its
// day falls in under the card's current settings.
func (r Recurrence) Occurrences(refs TransactionRefs, through Date, max int, now time.Time) ([]Transaction, Date) {
	var out []Transaction
	next := r.Next
	for !next.IsZero() && !through.Before(next) && len(out) < max {
		t := r.template(next)
		t.Recurrence = &RecurrenceLink{ID: r.ID, Date: next}
		t.InvoiceMonth = invoiceOn(refs.Account, r.AccountID, next, (*CardSettings).InvoiceFor)
		if r.Kind == TransactionTransfer {
			t.ToInvoiceMonth = invoiceOn(refs.ToAccount, r.ToAccountID, next, (*CardSettings).PaymentInvoice)
		}
		t.CreatedAt, t.UpdatedAt = now.UTC(), now.UTC()
		out = append(out, t)
		next, _ = r.Rule.NextOn(next.AddDays(1))
	}
	return out, next
}

// Between returns the transactions r is still to create from day from
// through day to (both included): those from its next occurrence on, so
// never one already created. It is a projection, with no invoice; an ended
// recurrence has none.
func (r Recurrence) Between(from, to Date, now time.Time) []Transaction {
	if !r.Active() || to.Before(from) {
		return nil
	}
	p := r
	if p.Next.Before(from) {
		next, ok := r.Rule.NextOn(from)
		if !ok {
			return nil
		}
		p.Next = next
	}
	// At most one a day.
	ts, _ := p.Occurrences(TransactionRefs{}, to, to.daysSince(from)+1, now)
	return ts
}

// invoiceOn is the invoice of a leg on account id on day: the one pick
// finds when acc (what the caller found for id) is a credit card, none
// otherwise.
func invoiceOn(acc *Account, id string, day Date, pick func(*CardSettings, Date) Month) Month {
	if acc == nil || acc.ID != id || !acc.IsCard() {
		return Month{}
	}
	return pick(acc.Card, day)
}

// Follow applies to r the changes a scoped edit made to one of its
// occurrences (before and after the edit), so the next occurrences take
// them too: the amounts, the category, the description, the tags and the
// accounts (with their currencies). It returns whether r changed.
func (r Recurrence) Follow(before, after Transaction, now time.Time) (Recurrence, bool) {
	t := carryChanges(r.template(Date{}), before, after)
	t = carryTransfer(t, before, after)
	next := r
	next.setTemplate(t)
	if next.equal(r) {
		return r, false
	}
	next.UpdatedAt = now.UTC()
	return next, true
}

// EndBefore ends r on the day before day, so it creates nothing from day
// on. When that leaves the rule no day at all (day is on or before its
// start), it returns false and the whole recurrence goes, unless
// hasEarlier: occurrences from before day remain (they came before a start
// moved past them), so r stays as an ended rule, with no next occurrence
// and an end before its start.
func (r Recurrence) EndBefore(day Date, hasEarlier bool, now time.Time) (Recurrence, bool) {
	if !r.Rule.Start.Before(day) && !hasEarlier {
		return Recurrence{}, false
	}
	next := r
	next.Tags = slices.Clone(r.Tags)
	if end := day.AddDays(-1); next.Rule.End.IsZero() || end.Before(next.Rule.End) {
		next.Rule.End = end
	}
	if next.Active() && next.Rule.End.Before(next.Next) {
		next.Next = Date{}
	}
	if !next.equal(r) {
		next.UpdatedAt = now.UTC()
	}
	return next, true
}
