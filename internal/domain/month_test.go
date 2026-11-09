package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func month(t *testing.T, s string) Month {
	t.Helper()
	m, err := ParseMonth(s)
	require.NoError(t, err, s)
	return m
}

func day(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	require.NoError(t, err, s)
	return d
}

func TestParseMonth(t *testing.T) {
	m := month(t, "2026-12")
	assert.Equal(t, "2026-12", m.String())
	assert.Equal(t, 2026, m.Year())
	assert.Equal(t, time.December, m.Month())
	assert.False(t, m.IsZero())

	for _, s := range []string{"1900-01", "2100-12", "2026-01"} {
		assert.Equal(t, s, month(t, s).String())
	}
	for s, code := range map[string]string{
		"":           CodeRequired,
		"2026-1":     CodeInvalid,
		"2026-13":    CodeInvalid,
		"2026-00":    CodeInvalid,
		"2026-12-01": CodeInvalid,
		"12/2026":    CodeInvalid,
		" 2026-12":   CodeInvalid,
		"1899-12":    CodeOutOfRange,
		"2101-01":    CodeOutOfRange,
	} {
		_, err := ParseMonth(s)
		assert.Equal(t, code, codeOf(t, err), s)
	}
}

func TestMonthArithmetic(t *testing.T) {
	nov := month(t, "2026-11")
	assert.Equal(t, "2027-01", nov.AddMonths(2).String(), "across the year")
	assert.Equal(t, "2025-12", nov.AddMonths(-11).String())
	assert.Equal(t, "2026-11", nov.AddMonths(0).String())
	assert.Equal(t, 14, month(t, "2028-01").Sub(nov))
	assert.Equal(t, -14, nov.Sub(month(t, "2028-01")))
	assert.True(t, nov.Before(nov.AddMonths(1)))
	assert.False(t, nov.Before(nov))
	assert.Equal(t, nov, NewMonth(2026, time.November), "months compare with ==")
	assert.Equal(t, month(t, "2027-01"), NewMonth(2026, 13), "a month past December carries into the next year")
	assert.Equal(t, nov, MonthOf(day(t, "2026-11-30")))
	assert.Equal(t, "1900-01", MinMonth.String())
	assert.Equal(t, "2100-12", MaxMonth.String())

	var zero Month
	assert.True(t, zero.IsZero())
	assert.Empty(t, zero.String())
}

func TestMonthDay(t *testing.T) {
	for _, tc := range []struct {
		month string
		day   int
		want  string
	}{
		{"2026-11", 8, "2026-11-08"},
		{"2026-11", 31, "2026-11-30"},
		{"2027-02", 31, "2027-02-28"},
		{"2028-02", 31, "2028-02-29"},
		{"2028-02", 29, "2028-02-29"},
		{"2026-12", 31, "2026-12-31"},
		{"2026-12", 0, "2026-12-01"},
	} {
		assert.Equal(t, tc.want, month(t, tc.month).Day(tc.day).String(), "%s day %d", tc.month, tc.day)
	}
}

func TestMonthLastDay(t *testing.T) {
	for m, want := range map[Month]string{
		NewMonth(2026, time.November): "2026-11-30",
		NewMonth(2026, time.December): "2026-12-31",
		NewMonth(2027, time.February): "2027-02-28",
		NewMonth(2028, time.February): "2028-02-29",
	} {
		assert.Equal(t, want, m.LastDay().String(), m.String())
	}
}
