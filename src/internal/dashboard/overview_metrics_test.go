package dashboard

import (
	"strings"
	"testing"
)

// The metrics block must state which unit each figure is: a throughput labelled
// "tasks/min" that was really completed/24 is what made the numbers look
// impossible. Task figures are the headline; runs are the detail.
func TestOverviewRendersTaskAndRunMetricsSeparately(t *testing.T) {
	a := newTestApp(140, 60)
	a.model.overviewTab = &OverviewTab{
		Status:         "Healthy",
		CompletedToday: 53,
		FailedToday:    4,
		AvgDuration:    "3.6m",
		SuccessRate:    "93.0%",
		TasksPerDay:    "3.0",
		RunsTotal:      58,
		RunsCompleted:  55,
		RunsFailed:     1,
		RunsRunning:    2,
		AvgRunDuration: "3.5m",
		RunsPerDay:     "3.3",
	}

	out := stripANSI(a.renderOverviewTab(60))
	for _, want := range []string{
		"Tasks (last 7 days)",
		"Runs (last 7 days)",
		"55 ok · 1 failed · 2 running",
		"3.0 tasks/day",
		"3.6m per task",
		"93.0% of finished tasks",
		"3.3 runs/day",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tasks/min") {
		t.Error("throughput must not be labelled per minute")
	}
}

// The duration formatter is what stops a five-hour task reading as "21600s".
func TestFormatMetricDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "—"},
		{42_000, "42s"},
		{90_000, "1.5m"},
		{3_600_000, "1.0h"},
		{21_600_000, "6.0h"},
	}
	for _, tc := range cases {
		if got := formatMetricDuration(tc.ms); got != tc.want {
			t.Errorf("formatMetricDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
