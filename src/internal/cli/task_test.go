package cli

import (
	"errors"
	"testing"
)

// TestTaskHistoryErrorMessage covers the #471 CLI-side bug: ipcGetJSON wraps a
// non-2xx /tasks/history response as "<HTTP status>: <body>" (e.g. "404 Not
// Found: task not found: ..."), and the old code matched the lowercase substring
// "not found" against that whole string — which never matched "404 Not Found:
// ...", so an unresolved reference fell through to the daemon-down message
// instead of reporting what actually happened. taskHistoryErrorMessage strips
// the transport prefix so the CLI can show the daemon's real answer.
func TestTaskHistoryErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "unresolved reference reports the reference, not the transport",
			err:  errors.New(`404 Not Found: task not found: "PSP-278" in source rl-jira`),
			want: `task not found: "PSP-278" in source rl-jira`,
		},
		{
			name: "bound item with no workflow history yet keeps the friendly message",
			err:  errors.New("404 Not Found: task history not found"),
			want: "No task history found.",
		},
		{
			name: "a message with no status prefix passes through unchanged",
			err:  errors.New("no database"),
			want: "no database",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskHistoryErrorMessage(tc.err); got != tc.want {
				t.Errorf("taskHistoryErrorMessage(%q) = %q, want %q", tc.err.Error(), got, tc.want)
			}
		})
	}
}

// TestIsDaemonDown_DoesNotMatchNotFoundResponses guards the actual root cause of
// #471's misleading error: a 404 body containing "Not Found" (capitalized, from
// http.StatusText) must never be classified as the daemon being unreachable —
// only a real transport failure (no socket, connection refused) should be.
func TestIsDaemonDown_DoesNotMatchNotFoundResponses(t *testing.T) {
	notDown := []error{
		errors.New(`404 Not Found: task not found: "PSP-278" in source rl-jira`),
		errors.New("404 Not Found: task history not found"),
	}
	for _, err := range notDown {
		if isDaemonDown(err) {
			t.Errorf("isDaemonDown(%q) = true, want false", err.Error())
		}
	}

	down := []error{
		errors.New("dial unix /path/apiary.sock: connect: no such file or directory"),
		errors.New("dial unix /path/apiary.sock: connect: connection refused"),
	}
	for _, err := range down {
		if !isDaemonDown(err) {
			t.Errorf("isDaemonDown(%q) = false, want true", err.Error())
		}
	}
}
