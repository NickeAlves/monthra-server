package domain

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// FixedDecimals is how many decimal places a Quantity or a Price holds: 8,
// like a satoshi, enough for crypto fractions and fund quotas.
const FixedDecimals = 8

// fixedOne is 1 in the fixed-point scale of Quantity and Price.
const fixedOne int64 = 100_000_000

// Bounds of the fixed-point numbers: 10^10 units, 10^18 in their scale, well
// inside an int64.
const (
	MaxQuantity Quantity = 10_000_000_000 * Quantity(fixedOne)
	MaxPrice    Price    = 10_000_000_000 * Price(fixedOne)
)

// Quantity is how many units of an asset, in 10^-8 units: "0.5" is
// 50_000_000. The API writes it as a decimal string (no float ever holds
// it), MongoDB as a Decimal128.
type Quantity int64

// Price is what one unit of an asset is worth, in 10^-8 major units of its
// currency (not cents: one unit may be worth a fraction of a cent): "38.42"
// is 3_842_000_000. Like Quantity, it travels as a decimal string.
type Price int64

// fixedPattern is a non-negative decimal with up to 8 decimal places: no
// sign, no exponent, no grouping.
var fixedPattern = regexp.MustCompile(`^[0-9]{1,11}(\.[0-9]{1,8})?$`)

// parseFixed reads a decimal string in the fixed-point scale, up to max.
func parseFixed(s string, maxValue int64) (int64, error) {
	if s == "" {
		return 0, &fieldErr{CodeRequired}
	}
	if !fixedPattern.MatchString(s) {
		return 0, &fieldErr{CodeInvalid}
	}
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > maxValue/fixedOne {
		return 0, &fieldErr{CodeOutOfRange}
	}
	f := int64(0)
	if frac != "" {
		f, _ = strconv.ParseInt(frac+strings.Repeat("0", FixedDecimals-len(frac)), 10, 64)
	}
	v := w*fixedOne + f
	if v > maxValue {
		return 0, &fieldErr{CodeOutOfRange}
	}
	return v, nil
}

// formatFixed writes v of the fixed-point scale as a decimal string with
// no trailing zeros: "0.5", "12", "0.00000001".
func formatFixed(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	whole := strconv.FormatInt(v/fixedOne, 10)
	frac := strings.TrimRight(strconv.FormatInt(fixedOne+v%fixedOne, 10)[1:], "0")
	if frac == "" {
		return sign + whole
	}
	return sign + whole + "." + frac
}

// ParseQuantity reads the quantity of a trade: above zero, up to
// MaxQuantity, at most 8 decimal places.
func ParseQuantity(s string) (Quantity, error) {
	v, err := parseFixed(s, int64(MaxQuantity))
	if err != nil {
		return 0, err
	}
	if v == 0 {
		return 0, &fieldErr{CodeOutOfRange}
	}
	return Quantity(v), nil
}

// ParsePrice reads a price: zero (a worthless asset) up to MaxPrice, at
// most 8 decimal places.
func ParsePrice(s string) (Price, error) {
	v, err := parseFixed(s, int64(MaxPrice))
	return Price(v), err
}

// String writes the quantity as a decimal string.
func (q Quantity) String() string { return formatFixed(int64(q)) }

// String writes the price as a decimal string.
func (p Price) String() string { return formatFixed(int64(p)) }

// centsPerFixedSquared is 10^14: a Quantity times a Price is in 10^-16
// major units, 10^-14 cents (the three currencies have two decimals).
var centsPerFixedSquared = big.NewInt(100_000_000_000_000)

// divRound divides n by d (both positive) rounding half up.
func divRound(n, d *big.Int) *big.Int {
	half := new(big.Int).Rsh(d, 1)
	q := new(big.Int).Add(n, half)
	return q.Quo(q, d)
}

// fitAmount returns v as an amount, if it is at most MaxAmount.
func fitAmount(v *big.Int) (int64, bool) {
	if !v.IsInt64() || v.Int64() > MaxAmount {
		return 0, false
	}
	return v.Int64(), true
}

// Value returns what q units are worth at price p, in minor units rounded
// half up, or false when that is past MaxAmount.
func (q Quantity) Value(p Price) (int64, bool) {
	n := new(big.Int).Mul(big.NewInt(int64(q)), big.NewInt(int64(p)))
	return fitAmount(divRound(n, centsPerFixedSquared))
}

// PriceOf returns the price of one unit when q units cost amount minor
// units, rounded half up, or false when that is past MaxPrice (a dust
// quantity sold for a fortune).
func PriceOf(amount int64, q Quantity) (Price, bool) {
	if q <= 0 || amount < 0 {
		return 0, false
	}
	n := new(big.Int).Mul(big.NewInt(amount), centsPerFixedSquared)
	p := divRound(n, big.NewInt(int64(q)))
	if !p.IsInt64() || p.Int64() > int64(MaxPrice) {
		return 0, false
	}
	return Price(p.Int64()), true
}

// shareOf returns amount * part / whole rounded half up: the cost of part
// of a position of whole units (whole > 0, part <= whole), which fits an
// int64 since it is at most amount.
func shareOf(amount, part, whole int64) int64 {
	n := new(big.Int).Mul(big.NewInt(amount), big.NewInt(part))
	return divRound(n, big.NewInt(whole)).Int64()
}
