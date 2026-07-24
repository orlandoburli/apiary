package editor

import (
	"fmt"
	"strings"
)

// DiffLine is one line of a unified diff.
type DiffLine struct {
	Kind DiffKind
	Text string
}

// DiffKind classifies a diff line.
type DiffKind int

const (
	DiffContext DiffKind = iota
	DiffAdded
	DiffRemoved
)

// ComputeDiff returns a unified-style diff between original and updated YAML strings.
// The algorithm is a simple LCS-based diff suitable for small workflow blocks.
func ComputeDiff(original, updated string) []DiffLine {
	a := strings.Split(original, "\n")
	b := strings.Split(updated, "\n")
	return lcs(a, b)
}

// lcs computes a diff via longest-common-subsequence.
func lcs(a, b []string) []DiffLine {
	m, n := len(a), len(b)
	// dp[i][j] = LCS length for a[:i], b[:j]
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var result []DiffLine
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1]:
			result = append(result, DiffLine{Kind: DiffContext, Text: a[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			result = append(result, DiffLine{Kind: DiffAdded, Text: b[j-1]})
			j--
		default:
			result = append(result, DiffLine{Kind: DiffRemoved, Text: a[i-1]})
			i--
		}
	}

	// Reverse (we built it backwards)
	for lo, hi := 0, len(result)-1; lo < hi; lo, hi = lo+1, hi-1 {
		result[lo], result[hi] = result[hi], result[lo]
	}
	return result
}

// RenderDiff renders the diff lines as a styled terminal string using ansi
// colors (no lipgloss dependency so this function is pure text).
func RenderDiff(lines []DiffLine, width int) string {
	var sb strings.Builder
	for _, l := range lines {
		var prefix, text string
		switch l.Kind {
		case DiffAdded:
			prefix = "+ "
			text = fmt.Sprintf("\033[32m%s%s\033[0m", prefix, truncateDiff(l.Text, width-2))
		case DiffRemoved:
			prefix = "- "
			text = fmt.Sprintf("\033[31m%s%s\033[0m", prefix, truncateDiff(l.Text, width-2))
		default:
			text = "  " + truncateDiff(l.Text, width-2)
		}
		sb.WriteString(text + "\n")
	}
	return sb.String()
}

func truncateDiff(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// DiffHasChanges reports whether a diff contains any added or removed lines.
func DiffHasChanges(lines []DiffLine) bool {
	for _, l := range lines {
		if l.Kind != DiffContext {
			return true
		}
	}
	return false
}
