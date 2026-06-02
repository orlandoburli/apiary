package dashboard

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// Colors
	ColorSuccess   = lipgloss.Color("42")   // Green
	ColorWarning   = lipgloss.Color("220")  // Yellow
	ColorError     = lipgloss.Color("196")  // Red
	ColorInfo      = lipgloss.Color("33")   // Blue
	ColorMuted     = lipgloss.Color("240")  // Gray
	ColorFocused   = lipgloss.Color("213")  // Magenta
	ColorBackground = lipgloss.Color("235") // Dark gray

	// Styles
	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorInfo).
			Padding(0, 1)

	StyleTab = lipgloss.NewStyle().
		Padding(0, 1)

	StyleActiveTab = StyleTab.
		Bold(true).
		Foreground(ColorFocused).
		Underline(true)

	StyleBorder = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorMuted).
		Padding(0, 1)

	StyleSuccess = lipgloss.NewStyle().
		Foreground(ColorSuccess)

	StyleError = lipgloss.NewStyle().
		Foreground(ColorError)

	StyleWarning = lipgloss.NewStyle().
		Foreground(ColorWarning)

	StyleInfo = lipgloss.NewStyle().
		Foreground(ColorInfo)

	StyleMuted = lipgloss.NewStyle().
		Foreground(ColorMuted)

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
	case "failed", "error":
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
