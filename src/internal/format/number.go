// Package format renders numbers for display. These helpers are presentation
// only — stored and transported values stay exact; nothing here should be parsed
// back or used in arithmetic.
package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// unit thresholds for compact token counts, largest first.
var tokenUnits = []struct {
	limit float64
	sufix string
}{
	{1e12, "T"},
	{1e9, "B"},
	{1e6, "M"},
	{1e3, "k"},
}

// Tokens renders a token count compactly: 842, 1.5k, 65M, 1.2B.
//
// One decimal is kept below 10 units (1.5k) and dropped above it (65M), which
// is where the extra digit stops carrying information in a table cell. A value
// that rounds up to the next unit is promoted, so 999,999 renders as "1M" rather
// than "1000k".
func Tokens(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	v := float64(n)
	if v < 0 {
		sign, v = "-", -v
	}
	for i, u := range tokenUnits {
		if v < u.limit {
			continue
		}
		scaled := v / u.limit
		// Rounding can push the value into the next unit up (999,999 → 1000.0k).
		if rounded := roundTo(scaled, 1); rounded >= 1000 && i > 0 {
			u = tokenUnits[i-1]
			scaled = v / u.limit
		}
		return sign + trimFloat(scaled) + u.sufix
	}
	return sign + strconv.FormatInt(int64(v), 10)
}

// TokensDelta renders a signed token difference: +1.5k, -840, 0.
func TokensDelta(n int) string {
	if n > 0 {
		return "+" + Tokens(n)
	}
	// Tokens already carries the minus sign for negatives.
	return Tokens(n)
}

// USD renders a monetary amount in US dollars: $1,234.56, $0.0425, -$3.20.
//
// Amounts below a dollar keep four decimals, because per-step agent costs are
// routinely fractions of a cent and rounding them to $0.00 would erase the only
// figure on the row. Anything smaller than the smallest representable amount
// renders as "<$0.0001" rather than a misleading zero.
func USD(v float64) string {
	if v == 0 || math.IsNaN(v) {
		return "$0.00"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	if v < 1 {
		if v < 0.00005 {
			return sign + "<$0.0001"
		}
		return sign + "$" + strconv.FormatFloat(v, 'f', 4, 64)
	}
	whole, frac := math.Modf(v)
	cents := int(math.Round(frac * 100))
	if cents == 100 { // 1.999 → 2.00, not 1.100
		whole, cents = whole+1, 0
	}
	return sign + "$" + group(int64(whole)) + fmt.Sprintf(".%02d", cents)
}

// USDDelta renders a signed monetary difference: +$0.0425, -$3.20, $0.00.
func USDDelta(v float64) string {
	if v > 0 {
		return "+" + USD(v)
	}
	// USD already carries the minus sign for negatives.
	return USD(v)
}

// Count renders an exact integer with thousands separators: 98,180,082. Use it
// where the precise figure matters; prefer Tokens for table cells.
func Count(n int) string {
	if n < 0 {
		return "-" + group(int64(-n))
	}
	return group(int64(n))
}

// group inserts thousands separators into a non-negative integer.
func group(n int64) string {
	digits := strconv.FormatInt(n, 10)
	if len(digits) <= 3 {
		return digits
	}
	var parts []string
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	return strings.Join(append([]string{digits}, parts...), ",")
}

// trimFloat renders a scaled value with one decimal below 10 and none above,
// dropping a trailing ".0" so 1000 reads as "1k" rather than "1.0k".
func trimFloat(v float64) string {
	if v >= 10 {
		return strconv.FormatInt(int64(math.Round(v)), 10)
	}
	s := strconv.FormatFloat(roundTo(v, 1), 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func roundTo(v float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	return math.Round(v*shift) / shift
}
