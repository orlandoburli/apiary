package config

import (
	"strings"
	"testing"
)

func TestValidateQueueSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings QueueSettings
		contains string
	}{
		{"listener requires token", QueueSettings{Listen: ":8080"}, "worker_token is required"},
		{"heartbeat before lease", QueueSettings{LeaseDuration: "5s", HeartbeatInterval: "5s"}, "must be shorter"},
		{"positive scoped limit", QueueSettings{Concurrency: QueueConcurrencySettings{Pools: map[string]int{"build": 0}}}, "positive limit"},
		{"positive workflow limit", QueueSettings{Concurrency: QueueConcurrencySettings{Workflows: map[string]int{"sweep": 0}}}, "positive limit"},
		{"non-negative default workflow", QueueSettings{Concurrency: QueueConcurrencySettings{DefaultWorkflow: -1}}, "must be >= 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateQueueSettings(tc.settings)
			if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.contains) {
				t.Fatalf("errors=%v, want %q", errs, tc.contains)
			}
		})
	}
	if errs := validateQueueSettings(QueueSettings{Listen: "127.0.0.1:8080", WorkerToken: "secret", LeaseDuration: "30s", HeartbeatInterval: "10s", WorkerCapacity: 4}); len(errs) != 0 {
		t.Fatalf("valid settings: %v", errs)
	}
}
