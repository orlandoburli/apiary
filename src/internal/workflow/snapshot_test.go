package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestRunInstancePersistsSnapshotWithoutEnvironmentSecrets(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	cfg := baseCfg()
	wf := config.WorkflowConfig{
		ID: "feature", Env: map[string]string{"TOKEN": "workflow-secret"},
		Steps: []config.StepConfig{{ID: "run", Agent: "backend-dev", Env: map[string]string{"KEY": "step-secret"}}},
	}
	eng := testEngine(cfg, dbc, &fakeExecutor{}, &fakeSide{})
	instanceID, _, err := eng.RunInstance(ctx, wf, model.InternalTask{ID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := dbc.GetWorkflowSnapshot(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot, "workflow-secret") || strings.Contains(snapshot, "step-secret") {
		t.Fatalf("snapshot leaked environment values: %s", snapshot)
	}
	var got config.WorkflowConfig
	if err := json.Unmarshal([]byte(snapshot), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != wf.ID || len(got.Steps) != 1 || got.Steps[0].ID != "run" {
		t.Fatalf("snapshot lost workflow structure: %+v", got)
	}
}
