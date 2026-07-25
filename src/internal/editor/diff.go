package editor

import (
	"fmt"
	"strings"
)

// unifiedDiff computes a simple unified-diff-style string between two text
// blobs. It uses LCS-based backtracking to emit context lines, additions, and
// deletions. The result is human-readable but not machine-parseable.
func unifiedDiff(original, proposed string) string {
	a := strings.Split(original, "\n")
	b := strings.Split(proposed, "\n")

	// Compute LCS length table.
	m, n := len(a), len(b)
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

	// Backtrack to build the edit script in reverse.
	type line struct {
		op   byte // ' ', '+', '-'
		text string
	}
	rev := make([]line, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			rev = append(rev, line{' ', a[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			rev = append(rev, line{'+', b[j-1]})
			j--
		} else {
			rev = append(rev, line{'-', a[i-1]})
			i--
		}
	}

	// Reverse.
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}

	// Count added/removed.
	added, removed := 0, 0
	for _, l := range rev {
		switch l.op {
		case '+':
			added++
		case '-':
			removed++
		}
	}

	if added == 0 && removed == 0 {
		return "(no semantic changes)"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- original  (%d removed, %d added)\n", removed, added))
	sb.WriteString("+++ proposed\n")
	for _, l := range rev {
		sb.WriteByte(l.op)
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}
