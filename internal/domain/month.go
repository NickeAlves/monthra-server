package domain

import (
	"fmt"
	"time"
)

// monthLayout is how a Month is written: an ISO 8601 calendar month.
const monthLayout = "2006-01"

// The span of months ParseMonth accepts: those of MinDate to MaxDate.
var (
	MinMonth = MonthOf(MinDate)
	MaxMonth = MonthOf(MaxDate)
)

// Month is a civil month, with no zone: the invoice of a card, and later the
// month of a dashboard or a budget. It is written "YYYY-MM"; the zero Month
// is no month. Months compare with ==.
type Month struct {
	// n is year*12 + month (1 to 12), so the zero Month names no month and
	// adding months is adding to n.
	n int
}

// NewMonth returns the month of year; a month outside 1 to 12 carries into
// the year before or after, as time.Date does.
func NewMonth(year int, month time.Month) Month { return Month{year*12 + int(month)} }

// MonthOf returns the month of day d.
func MonthOf(d Date) Month { return NewMonth(d.t.Year(), d.t.Month()) }

// ParseMonth reads "YYYY-MM" strictly (a two-digit month) and checks it lies
// between MinMonth and MaxMonth.
func ParseMonth(s string) (Month, error) {
	if s == "" {
		return Month{}, &fieldErr{CodeRequired}
	}
	t, err := time.Parse(monthLayout, s)
	if err != nil {
		return Month{}, &fieldErr{CodeInvalid}
	}
	m := NewMonth(t.Year(), t.Month())
	if m.Before(MinMonth) || MaxMonth.Before(m) {
		return Month{}, &fieldErr{CodeOutOfRange}
	}
	return m, nil
}

// Year returns the year of m.
func (m Month) Year() int { return (m.n - 1) / 12 }

// Month returns the month of the year of m.
func (m Month) Month() time.Month { return time.Month((m.n-1)%12 + 1) }

// String writes the month as "YYYY-MM", or "" for the zero Month.
func (m Month) String() string {
	if m.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d", m.Year(), int(m.Month()))
}

// IsZero reports whether m is the zero Month (no month).
func (m Month) IsZero() bool { return m.n == 0 }

// Before reports whether m comes before o.
func (m Month) Before(o Month) bool { return m.n < o.n }

// AddMonths returns the month k months after m (before it when k < 0).
func (m Month) AddMonths(k int) Month { return Month{m.n + k} }

// Sub returns how many months m comes after o (negative when before).
func (m Month) Sub(o Month) int { return m.n - o.n }

// Day returns day d of m, or the last day of m when m is shorter: day 31
// of February is February 28 (29 in a leap year).
func (m Month) Day(d int) Date {
	last := time.Date(m.Year(), m.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	d = min(max(d, 1), last)
	return Date{time.Date(m.Year(), m.Month(), d, 0, 0, 0, 0, time.UTC)}
}
