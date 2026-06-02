package styles

import "github.com/charmbracelet/lipgloss"

var (
	ColorBrand   = lipgloss.Color("#F5A623")
	ColorMuted   = lipgloss.Color("#626262")
	ColorSuccess = lipgloss.Color("#04B575")
	ColorError   = lipgloss.Color("#FF4672")
	ColorWarning = lipgloss.Color("#ECFD65")
	ColorText    = lipgloss.Color("#DDDDDD")
	ColorBg      = lipgloss.Color("#1A1A2E")
	ColorPanel   = lipgloss.Color("#16213E")
	ColorBorder  = lipgloss.Color("#2D2D4E")
	ColorHighlight = lipgloss.Color("#0F3460")

	Header = lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorBrand).
		Padding(0, 1)

	Tab = lipgloss.NewStyle().
		Padding(0, 2).
		Foreground(ColorMuted)

	TabActive = lipgloss.NewStyle().
		Padding(0, 2).
		Foreground(ColorBrand).
		Bold(true).
		Underline(true)

	StatusBar = lipgloss.NewStyle().
		Foreground(ColorMuted).
		Padding(0, 1)

	StatusBarKey = lipgloss.NewStyle().
		Foreground(ColorBrand).
		Bold(true)

	Panel = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	Badge = func(color lipgloss.Color) lipgloss.Style {
		return lipgloss.NewStyle().
			Background(color).
			Foreground(lipgloss.Color("#000000")).
			Padding(0, 1).
			Bold(true)
	}

	Running = Badge(ColorSuccess)
	Failed  = Badge(ColorError)
	Pending = Badge(ColorWarning)
	Done    = Badge(ColorMuted)
)
