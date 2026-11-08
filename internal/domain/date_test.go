package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDate(t *testing.T) {
	d, err := ParseDate("2026-11-30")
	require.NoError(t, err)
	assert.Equal(t, "2026-11-30", d.String())
	assert.Equal(t, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), d.Time(), "midnight UTC")
	assert.False(t, d.IsZero())

	for _, s := range []string{"1900-01-01", "2100-12-31", "2028-02-29"} {
		got, err := ParseDate(s)
		require.NoError(t, err, s)
		assert.Equal(t, s, got.String())
	}
	for s, code := range map[string]string{
		"":                     CodeRequired,
		"2026-1-5":             CodeInvalid,
		"2026-02-30":           CodeInvalid,
		"2027-02-29":           CodeInvalid,
		"2026-11-08T10:00:00Z": CodeInvalid,
		"08/11/2026":           CodeInvalid,
		" 2026-11-08":          CodeInvalid,
		"1899-12-31":           CodeOutOfRange,
		"2101-01-01":           CodeOutOfRange,
	} {
		_, err := ParseDate(s)
		assert.Equal(t, code, codeOf(t, err), s)
	}
}

func TestDateOf(t *testing.T) {
	late := time.Date(2026, 11, 30, 23, 30, 0, 0, time.FixedZone("BRT", -3*3600))
	assert.Equal(t, "2026-11-30", DateOf(late).String(), "the day on the instant's own clock")
	assert.Equal(t, "2026-12-01", DateOf(late.UTC()).String())
	assert.True(t, Date{}.IsZero())

	a, b := DateOf(late), DateOf(late.UTC())
	assert.True(t, a.Before(b))
	assert.False(t, b.Before(a))
	assert.True(t, a.Equal(DateOf(late.Add(-time.Hour))))
	assert.False(t, a.Equal(b))
}
