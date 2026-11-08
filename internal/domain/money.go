package domain

// MaxAmount bounds every amount, in minor units of its currency: 10^13
// cents is 100 billion in BRL, USD or EUR. Clients whose numbers are IEEE
// doubles (JavaScript, Dart on the web) hold integers exactly only up to
// 2^53 (about 9 * 10^15), so totals of hundreds of such amounts stay exact.
const MaxAmount int64 = 10_000_000_000_000

// CheckAmount validates an amount in minor units (int64 cents, never a
// float): at most MaxAmount in absolute value, and not negative unless
// allowNegative.
func CheckAmount(v int64, allowNegative bool) error {
	if v > MaxAmount || v < -MaxAmount || (v < 0 && !allowNegative) {
		return &fieldErr{CodeOutOfRange}
	}
	return nil
}
