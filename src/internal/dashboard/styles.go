package dashboard

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// Palette
	ColorSuccess    = lipgloss.Color("42")  // Green
	ColorWarning    = lipgloss.Color("220") // Yellow
	ColorError      = lipgloss.Color("203") // Red
	ColorInfo       = lipgloss.Color("39")  // Blue
	ColorMuted      = lipgloss.Color("244") // Gray
	ColorDim        = lipgloss.Color("240") // Dim gray
	ColorFocused    = lipgloss.Color("213") // Magenta
	ColorAccent     = lipgloss.Color("117") // Light cyan
	ColorTitle      = lipgloss.Color("81")  // Cyan
	ColorBorder     = lipgloss.Color("60")  // Slate
	ColorText       = lipgloss.Color("252") // Near-white
	ColorBackground = lipgloss.Color("236") // Dark gray
	ColorSelBg      = lipgloss.Color("24")  // Dark cyan (selection bar)
	ColorTabBg      = lipgloss.Color("57")  // Indigo (active tab)
	ColorFooterBg   = lipgloss.Color("236") // Footer bar bg

	// Brand title in the tab bar.
	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")).
			Background(ColorFocused).
			Padding(0, 1)

	// Inactive / active tab chips.
	StyleTab = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Padding(0, 2)

	StyleActiveTab = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")).
			Background(ColorTabBg).
			Padding(0, 2)

	StyleBorder = lipgloss.NewStyle().Foreground(ColorBorder)

	// Box title shown in the top border.
	StyleBoxTitle = lipgloss.NewStyle().Bold(true).Foreground(ColorTitle)

	// Table column header row.
	StyleTableHeader = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	// Selected list row (highlight bar) and its cursor arrow.
	StyleSelectedRow  = lipgloss.NewStyle().Background(ColorSelBg).Foreground(lipgloss.Color("231"))
	StyleFocusedArrow = lipgloss.NewStyle().Bold(true).Foreground(ColorFocused)

	// Detail view label / strong value.
	StyleLabel       = lipgloss.NewStyle().Foreground(ColorAccent)
	StyleValueStrong = lipgloss.NewStyle().Bold(true).Foreground(ColorText)

	// Task reference number (e.g. "ERP-42").
	StyleAccent = lipgloss.NewStyle().Foreground(ColorAccent)

	// Footer: each key is a small highlighted chip, labels are dim text.
	StyleFooterKey = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(ColorTabBg)
	StyleFooterLbl = lipgloss.NewStyle().Foreground(ColorText)
	StyleFooterDim = lipgloss.NewStyle().Foreground(ColorDim)

	StyleSuccess = lipgloss.NewStyle().Foreground(ColorSuccess)
	StyleError   = lipgloss.NewStyle().Foreground(ColorError)
	StyleWarning = lipgloss.NewStyle().Foreground(ColorWarning)
	StyleInfo    = lipgloss.NewStyle().Foreground(ColorInfo)
	StyleMuted   = lipgloss.NewStyle().Foreground(ColorMuted)

	StyleHighlight = lipgloss.NewStyle().
			Background(ColorFocused).
			Foreground(lipgloss.Color("0"))

	StyleCode = lipgloss.NewStyle().
			Background(ColorBackground).
			Foreground(ColorInfo).
			Padding(0, 1)
)

// StatusColor returns color based on status string.
func StatusColor(status string) string {
	switch status {
	case "success", "completed", "active":
		return StyleSuccess.Render("●")
	case "stale":
		return StyleWarning.Render("◉")
	case "zombie", "failed", "error":
		return StyleError.Render("●")
	case "running":
		return StyleWarning.Render("⟳")
	case "idle", "pending":
		return StyleMuted.Render("○")
	default:
		return StyleMuted.Render("?")
	}
}

// ProgressBar renders a simple progress bar.
func ProgressBar(percent int, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := (width * percent) / 100
	empty := width - filled

	bar := "[" +
		lipgloss.NewStyle().Foreground(ColorSuccess).Render(
			repeatString("=", filled)) +
		repeatString("-", empty) +
		"]"

	return bar + " " + lipgloss.NewStyle().Foreground(ColorMuted).Render(
		repeatString(" ", 3-len(string(rune(percent/10))))) +
		string(rune(48+percent/10)) + "%"
}

func repeatString(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
