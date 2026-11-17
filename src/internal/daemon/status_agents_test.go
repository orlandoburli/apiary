package daemon

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// The status endpoint's "agents active" must count the agent processes that
// are actually running.
//
// It used to count the dispatch slots an agent's semaphore held, which
// understates the work in flight: a workflow instance takes one slot for its
// whole run, but the run may fan out to several agent processes at once. On a
// live hive it said "engineer: 2" while three engineers were running, and
// "active: 1" while three processes worked — so the number a user read to
// decide whether the hive had spare capacity was wrong in the direction that
// matters.
func TestStatusReportsLiveAgentProcesses(t *testing.T) {
	ctx := context.Background()
	dbc := openDropTestDB(t, "status.db")
	d := &Dispatcher{
		db:            dbc,
		dispatchQueue: dbc.Queue(),
		cfg: &config.Config{
			Settings: config.Settings{Concurrency: 6},
			Agents: []config.AgentConfig{
				{ID: "engineer", MaxWorkers: 3},
				{ID: "qa", MaxWorkers: 3},
			},
		},
	}

	// Two engineer processes running at once (a fan-out of one workflow) and
	// one qa process.
	for i, spec := range []struct {
		task  string
		agent string
	}{{"T-1", "engineer"}, {"T-2", "engineer"}, {"T-3", "qa"}} {
		exec, err := dbc.CreateExecution(ctx, spec.task, spec.agent, "work", spec.task, "", "m", "cli", 1)
		if err != nil {
			t.Fatalf("create execution %s: %v", spec.task, err)
		}
		if err := dbc.SetPID(ctx, exec.ID, 1000+i); err != nil {
			t.Fatalf("set pid: %v", err)
		}
		if err := dbc.SendHeartbeat(ctx, exec.ID); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
	}

	resp := d.Status()
	if got := resp.Concurrency.AgentActive["engineer"]; got != 2 {
		t.Errorf("engineer active = %d, want 2 (two engineer processes are running)", got)
	}
	if got := resp.Concurrency.AgentActive["qa"]; got != 1 {
		t.Errorf("qa active = %d, want 1", got)
	}
	// An agent with nothing running must still be reported, as zero.
	if got, ok := resp.Concurrency.AgentActive["reviewer"]; ok && got != 0 {
		t.Errorf("an unconfigured agent reported %d", got)
	}
}
