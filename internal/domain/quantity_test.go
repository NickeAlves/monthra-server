package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]Quantity{
		"1":            100_000_000,
		"0.5":          50_000_000,
		"0.00000001":   1,
		"12.34567891":  1_234_567_891,
		"007":          700_000_000,
		"10000000000":  MaxQuantity,
		"9999999999.9": 999_999_999_990_000_000,
	} {
		q, err := ParseQuantity(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, q, in)
	}
	for in, want := range map[string]string{
		"":                       CodeRequired,
		"0":                      CodeOutOfRange,
		"0.00000000":             CodeOutOfRange,
		"10000000000.00000001":   CodeOutOfRange,
		"99999999999":            CodeOutOfRange,
		"0.000000001":            CodeInvalid,
		"-1":                     CodeInvalid,
		"+1":                     CodeInvalid,
		"1e3":                    CodeInvalid,
		"1,5":                    CodeInvalid,
		".5":                     CodeInvalid,
		"5.":                     CodeInvalid,
		" 1":                     CodeInvalid,
		"1 000":                  CodeInvalid,
		"123456789012":           CodeInvalid,
		"１":                      CodeInvalid,
		"0x10":                   CodeInvalid,
		"NaN":                    CodeInvalid,
		"100000000000.000000001": CodeInvalid,
	} {
		_, err := ParseQuantity(in)
		assert.Equal(t, want, codeOf(t, err), in)
	}
}

func TestParsePrice(t *testing.T) {
	p, err := ParsePrice("0")
	require.NoError(t, err)
	assert.Equal(t, Price(0), p, "a worthless asset")
	p, err = ParsePrice("38.42")
	require.NoError(t, err)
	assert.Equal(t, Price(3_842_000_000), p)
	p, err = ParsePrice("10000000000")
	require.NoError(t, err)
	assert.Equal(t, MaxPrice, p)
	_, err = ParsePrice("10000000000.1")
	assert.Equal(t, CodeOutOfRange, codeOf(t, err))
	_, err = ParsePrice("")
	assert.Equal(t, CodeRequired, codeOf(t, err))
}

func TestFixedString(t *testing.T) {
	for v, want := range map[int64]string{
		0:                     "0",
		1:                     "0.00000001",
		50_000_000:            "0.5",
		100_000_000:           "1",
		1_234_567_891:         "12.34567891",
		3_842_000_000:         "38.42",
		int64(MaxQuantity):    "10000000000",
		-150_000_000:          "-1.5",
		9_999_999_999_900_000: "99999999.999",
	} {
		assert.Equal(t, want, Quantity(v).String(), v)
		assert.Equal(t, want, Price(v).String(), v)
	}
	for _, s := range []string{"0.5", "12.34567891", "38.42", "10000000000", "0.00000001"} {
		q, err := ParseQuantity(s)
		require.NoError(t, err)
		assert.Equal(t, s, q.String(), "round trip")
	}
}

func TestQuantityValue(t *testing.T) {
	q := func(s string) Quantity {
		v, err := ParseQuantity(s)
		require.NoError(t, err, s)
		return v
	}
	p := func(s string) Price {
		v, err := ParsePrice(s)
		require.NoError(t, err, s)
		return v
	}
	for _, tc := range []struct {
		q, p string
		want int64
	}{
		{"100", "38.42", 384200},
		{"3", "33.33333333", 10000},      // 99.99999999 rounds to 100.00
		{"1", "0.005", 1},                // half a cent rounds up
		{"1", "0.00499999", 0},           // below half a cent rounds down
		{"0.01234567", "350000", 432098}, // 4320.9845 -> 4320.98
		{"0.5", "0", 0},
	} {
		v, ok := q(tc.q).Value(p(tc.p))
		require.True(t, ok, tc)
		assert.Equal(t, tc.want, v, tc)
	}
	_, ok := MaxQuantity.Value(MaxPrice)
	assert.False(t, ok, "past MaxAmount")
	v, ok := q("100").Value(p("1000000000"))
	require.True(t, ok)
	assert.Equal(t, MaxAmount, v, "MaxAmount itself fits")
}

func TestPriceOf(t *testing.T) {
	price, ok := PriceOf(10000, 300_000_000)
	require.True(t, ok)
	assert.Equal(t, "33.33333333", price.String(), "R$ 100.00 for 3 units")
	price, ok = PriceOf(384200, 10_000_000_000)
	require.True(t, ok)
	assert.Equal(t, "38.42", price.String())
	price, ok = PriceOf(1, MaxQuantity)
	require.True(t, ok)
	assert.Equal(t, Price(0), price, "a cent for 10^10 units rounds to nothing")

	_, ok = PriceOf(MaxAmount, 1)
	assert.False(t, ok, "a fortune for one hundred-millionth of a unit")
	_, ok = PriceOf(100, 0)
	assert.False(t, ok, "no units")
}

func TestShareOf(t *testing.T) {
	assert.Equal(t, int64(333), shareOf(1000, 1, 3))
	assert.Equal(t, int64(667), shareOf(1000, 2, 3), "half up")
	assert.Equal(t, int64(1000), shareOf(1000, 3, 3))
	assert.Equal(t, int64(5_000_000_000_000), shareOf(MaxAmount, int64(MaxQuantity)/2, int64(MaxQuantity)), "no overflow")
}
