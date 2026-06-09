package dashboard

import (
	"fmt"
	"strings"
)

type barItem struct {
	Label string
	Value float64
}

// barOpts configures barChart. The zero value renders dollar amounts with no
// percentage column.
type barOpts struct {
	maxWidth int
	// valueFmt renders the trailing value column. Defaults to formatVal (USD).
	valueFmt func(float64) string
	// showPct appends a "(NN%)" column.
	showPct bool
	// pctOfTotal bases the percentage on the sum of all values (share of total).
	// When false, the percentage is relative to the largest bar.
	pctOfTotal bool
}

// barChart renders a horizontal bar chart using colored blocks. Bar lengths are
// always scaled to the largest value so the biggest item fills the row; the
// value column and optional percentage are configurable via opts. Returns the
// rendered multi-line string.
func barChart(items []barItem, opts barOpts) string {
	if len(items) == 0 {
		return ""
	}
	valueFmt := opts.valueFmt
	if valueFmt == nil {
		valueFmt = formatVal
	}

	maxVal, sum := 0.0, 0.0
	for _, it := range items {
		if it.Value > maxVal {
			maxVal = it.Value
		}
		sum += it.Value
	}

	labelW := 0
	for _, it := range items {
		if len(it.Label) > labelW {
			labelW = len(it.Label)
		}
	}
	if labelW > 16 {
		labelW = 16
	}
	// Reserve room for the label, value, and percentage columns.
	barMax := opts.maxWidth - labelW - 24
	if barMax < 5 {
		barMax = 5
	}

	var b strings.Builder
	for _, it := range items {
		barLen := 0
		if maxVal > 0 {
			barLen = int(float64(barMax) * it.Value / maxVal)
		}
		if barLen < 1 && it.Value > 0 {
			barLen = 1
		}
		bar := strings.Repeat("█", barLen)
		label := pad(truncate(it.Label, labelW), labelW)
		row := fmt.Sprintf("  %s %s %s", label, StyleChartBar.Render(bar), valueFmt(it.Value))
		if opts.showPct {
			basis := maxVal
			if opts.pctOfTotal {
				basis = sum
			}
			pct := 0
			if basis > 0 {
				pct = int(it.Value/basis*100 + 0.5)
			}
			row += fmt.Sprintf(" (%d%%)", pct)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// formatVal renders a USD value for chart columns.
func formatVal(v float64) string {
	switch {
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v > 0:
		return fmt.Sprintf("$%.4f", v)
	default:
		return "$0.00"
	}
}

// formatTokens renders a token count as an integer with thousands separators,
// e.g. 98180082 -> "98,180,082".
func formatTokens(v float64) string {
	n := int64(v)
	neg := n < 0
	if neg {
		n = -n
	}
	digits := fmt.Sprintf("%d", n)
	var parts []string
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	parts = append([]string{digits}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}
