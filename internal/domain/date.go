package domain

import "time"

// dateLayout is how a Date is written: an ISO 8601 calendar date.
const dateLayout = "2006-01-02"

// The span of days a Date may hold: wide enough for any household history
// and for installments years ahead, narrow enough to catch typos.
var (
	MinDate = Date{time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)}
	MaxDate = Date{time.Date(2100, time.December, 31, 0, 0, 0, 0, time.UTC)}
)

// Date is a civil day, with no time and no zone: a transaction on
// "2026-11-30" belongs to November wherever its owner is. It is stored as
// midnight UTC. Instants (createdAt, ...) stay time.Time in UTC.
type Date struct{ t time.Time }

// DateOf returns the day of t on t's own clock.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ParseDate reads "YYYY-MM-DD" strictly (two-digit month and day, a day the
// month has) and checks it lies between MinDate and MaxDate.
func ParseDate(s string) (Date, error) {
	if s == "" {
		return Date{}, &fieldErr{CodeRequired}
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, &fieldErr{CodeInvalid}
	}
	d := Date{t}
	if d.Before(MinDate) || MaxDate.Before(d) {
		return Date{}, &fieldErr{CodeOutOfRange}
	}
	return d, nil
}

// Today is the reader's today: asOf (their local day; zero is today in UTC)
// within a day of today in UTC, which every time zone's today is. So no
// reader reaches into the future by naming a later day.
func Today(asOf Date, now time.Time) Date {
	today := DateOf(now.UTC())
	switch {
	case asOf.IsZero():
		return today
	case asOf.Before(today.AddDays(-1)):
		return today.AddDays(-1)
	case today.AddDays(1).Before(asOf):
		return today.AddDays(1)
	default:
		return asOf
	}
}

// String writes the day as "YYYY-MM-DD".
func (d Date) String() string { return d.t.Format(dateLayout) }

// Time returns midnight UTC of the day.
func (d Date) Time() time.Time { return d.t }

// IsZero reports whether d is the zero Date (no day).
func (d Date) IsZero() bool { return d.t.IsZero() }

// Before reports whether d comes before o.
func (d Date) Before(o Date) bool { return d.t.Before(o.t) }

// Equal reports whether d and o are the same day.
func (d Date) Equal(o Date) bool { return d.t.Equal(o.t) }

// Day returns the day of the month of d.
func (d Date) Day() int { return d.t.Day() }

// AddDays returns the day n days later (earlier when n < 0).
func (d Date) AddDays(n int) Date { return Date{d.t.AddDate(0, 0, n)} }

// daysSince returns how many days d comes after o (negative when before).
func (d Date) daysSince(o Date) int { return int(d.t.Sub(o.t) / (24 * time.Hour)) }

// AddMonths returns the same day k months later (earlier when k < 0), or
// the last day of that month when it is shorter: January 31 plus one month
// is February 28 (29 in a leap year), plus two is March 31.
func (d Date) AddMonths(k int) Date { return MonthOf(d).AddMonths(k).Day(d.Day()) }
