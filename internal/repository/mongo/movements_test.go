package mongo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDecimalFixed(t *testing.T) {
	v, err := decimalFixed(nil)
	require.NoError(t, err)
	assert.Zero(t, v, "absent")

	for in, want := range map[string]int64{
		"0.01234567": 1_234_567,
		"1.00000000": 100_000_000,
		"1E+2":       10_000_000_000, // written by hand with a positive exponent
		"38.4200":    3_842_000_000,
		"0":          0,
	} {
		d, err := bson.ParseDecimal128(in)
		require.NoError(t, err, in)
		v, err := decimalFixed(&d)
		require.NoError(t, err, in)
		assert.Equal(t, want, v, in)
	}
	for _, in := range []string{"0.000000001", "1E+30", "NaN", "Infinity"} {
		d, err := bson.ParseDecimal128(in)
		require.NoError(t, err, in)
		_, err = decimalFixed(&d)
		assert.Error(t, err, in)
	}

	// The round trip of what the store writes.
	for _, v := range []int64{0, 1, 50_000_000, 1_000_000_000_000_000_000} {
		back, err := decimalFixed(fixedDecimal(v))
		require.NoError(t, err)
		assert.Equal(t, v, back)
	}
}
