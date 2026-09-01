package daemon

import (
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

func TestInstanceDuration(t *testing.T) {
	base := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Minute)

	cases := []struct {
		name    string
		inst    db.WorkflowInstance
		wantDur string
	}{
		{
			name:    "running uses now-created",
			inst:    db.WorkflowInstance{State: db.InstanceStateRunning, CreatedAt: base},
			wantDur: "02:00",
		},
		{
			name:    "approval_waiting uses now-created",
			inst:    db.WorkflowInstance{State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval), CreatedAt: base},
			wantDur: "02:00",
		},
		{
			name:    "done uses updated-created",
			inst:    db.WorkflowInstance{State: db.InstanceStateDone, CreatedAt: base, UpdatedAt: base.Add(90 * time.Second)},
			wantDur: "01:30",
		},
		{
			name:    "failed uses updated-created",
			inst:    db.WorkflowInstance{State: db.InstanceStateFailed, CreatedAt: base, UpdatedAt: base.Add(45 * time.Second)},
			wantDur: "45s",
		},
		{
			name:    "interrupted has no span",
			inst:    db.WorkflowInstance{State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonInterrupted), CreatedAt: base},
			wantDur: "—",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := instanceDuration(tc.inst, now)
			if got != tc.wantDur {
				t.Fatalf("instanceDuration = %q, want %q", got, tc.wantDur)
			}
		})
	}
}

func TestStepDuration(t *testing.T) {
	base := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	now := base.Add(time.Minute)
	started := base
	finished := base.Add(30 * time.Second)

	t.Run("pending step", func(t *testing.T) {
		if got := stepDuration(db.StepRun{}, now); got != "—" {
			t.Fatalf("got %q, want —", got)
		}
	})
	t.Run("finished step uses finished-started", func(t *testing.T) {
		got := stepDuration(db.StepRun{StartedAt: &started, FinishedAt: &finished}, now)
		if got != "30s" {
			t.Fatalf("got %q, want 30s", got)
		}
	})
	t.Run("running step uses now-started", func(t *testing.T) {
		got := stepDuration(db.StepRun{StartedAt: &started}, now)
		if got != "01:00" {
			t.Fatalf("got %q, want 01:00", got)
		}
	})
}

func TestInstanceSummary(t *testing.T) {
	base := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	v := db.WorkflowInstanceView{
		WorkflowInstance: db.WorkflowInstance{
			ID:         "wf_abc",
			WorkflowID: "feature-development",
			CellID:     "PLANE-142",
			State:      db.InstanceStateRunning,
			CreatedAt:  base,
		},
		Title: "Implement auth",
	}
	s := instanceSummary(v, now)
	if s.ID != "wf_abc" || s.Workflow != "feature-development" || s.CellID != "PLANE-142" {
		t.Fatalf("unexpected identity fields: %+v", s)
	}
	if s.Title != "Implement auth" {
		t.Fatalf("title = %q", s.Title)
	}
	if s.Started != "05:00 ago" {
		t.Fatalf("started = %q, want 05:00 ago", s.Started)
	}
	if s.Duration != "05:00" {
		t.Fatalf("duration = %q, want 05:00", s.Duration)
	}
}
