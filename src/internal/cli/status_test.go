package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/daemon"
)

func TestRenderStatusIncludesQueueAndWorkers(t *testing.T) {
	result := renderStatus(&daemon.StatusResponse{Version: "test", Queue: daemon.QueueStatus{Enabled: true, Jobs: map[string]int{"queued": 2, "leased": 1}, Workers: []daemon.QueueWorkerStatus{{ID: "build-1", Pool: "build", Ready: true, Healthy: true, Capacity: 4, ActiveJobs: 1, LastHeartbeat: "2s ago"}}}})
	for _, want := range []string{"Queue", "queued: 2", "leased: 1", "build-1", "jobs: 1/4", "heartbeat: 2s ago"} {
		if !strings.Contains(result, want) {
			t.Fatalf("status missing %q:\n%s", want, result)
		}
	}
}
