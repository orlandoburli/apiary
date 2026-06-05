package dashboard

import (
	"fmt"
	"strings"

	"github.com/guptarohit/asciigraph"
)

// barChart renders a horizontal bar chart using lipgloss colored blocks.
// Each bar shows the value and percentage. Returns the rendered multi-line string.
func barChart(items []barItem, maxWidth int) string {
	if len(items) == 0 {
		return ""
	}
	maxVal := 0.0
	for _, it := range items {
		if it.Value > maxVal {
			maxVal = it.Value
		}
	}
	if maxVal == 0 {
		return ""
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
	barMax := maxWidth - labelW - 20
	if barMax < 5 {
		barMax = 5
	}

	var b strings.Builder
	for _, it := range items {
		barLen := int(float64(barMax) * it.Value / maxVal)
		if barLen < 1 && it.Value > 0 {
			barLen = 1
		}
		bar := strings.Repeat("█", barLen)
		label := pad(truncate(it.Label, labelW), labelW)
		valStr := formatVal(it.Value)
		pct := int(it.Value / maxVal * 100)
		fmt.Fprintf(&b, "  %s %s %s (%d%%)\n", label, StyleChartBar.Render(bar), valStr, pct)
	}
	return b.String()
}

type barItem struct {
	Label string
	Value float64
}

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

// lineChart renders an ASCII line chart from a series of data points.
func lineChart(data []float64, width, height int, caption string) string {
	if len(data) == 0 {
		return ""
	}
	allZero := true
	for _, v := range data {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return ""
	}
	return asciigraph.Plot(data, asciigraph.Width(width), asciigraph.Height(height), asciigraph.Caption(caption))
}

// lineChartLabels renders a line chart with date labels below each point.
func lineChartLabels(data []float64, labels []string, width, height int, caption string) string {
	if len(data) == 0 {
		return ""
	}
	allZero := true
	for _, v := range data {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return ""
	}
	graph := asciigraph.Plot(data, asciigraph.Width(width), asciigraph.Height(height), asciigraph.Caption(caption))

	// Add date labels below the chart
	if len(labels) > 0 {
		lines := strings.Split(graph, "\n")
		if len(lines) > 0 {
			// Find the last line (axis line) and add label row below it
			// Simple approach: add a row with abbreviated labels
			graph += "\n" + formatLabels(labels, width)
		}
	}
	return graph
}

func formatLabels(labels []string, width int) string {
	if len(labels) == 0 {
		return ""
	}
	// Show first, last, and a few evenly spaced labels
	n := len(labels)
	step := 1
	if n > 6 {
		step = (n - 1) / 5
		if step < 1 {
			step = 1
		}
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i == 0 || i == n-1 || (i >= step && (i%step) == 0) {
			b.WriteString(truncate(labels[i], 5))
		} else {
			b.WriteString("     ")
		}
		if i < n-1 {
			b.WriteString("  ")
		}
	}
	return b.String()
}

// style helpers for charts
const (
	maxBarWidth = 40
	chartHeight = 8
)

