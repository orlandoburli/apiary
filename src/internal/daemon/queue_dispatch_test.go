package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/queue"
)

func TestSettleRemoteQueueJobIsDurableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	client, err := db.New(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	task := &model.InternalTask{ID: "task-1", Title: "Remote work"}
	if err := client.InternalTasks().CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	job := &queue.Job{IdempotencyKey: "task-1:0:build", TaskID: task.ID, WorkflowID: "build", SourceID: "github", PayloadVersion: 1, Payload: []byte(`{}`), MaxAttempts: 1}
	if _, err := client.Queue().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.State = queue.JobSucceeded
	dispatcher := &Dispatcher{db: client, dispatchQueue: client.Queue()}
	if err := dispatcher.settleRemoteQueueJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.settleRemoteQueueJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	stored, err := client.InternalTasks().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != model.TaskStateDone || stored.OutstandingWorkflows != 0 {
		t.Fatalf("task=%+v", stored)
	}
	instance, err := client.GetWorkflowInstance(ctx, "queue-"+job.ID)
	if err != nil || instance == nil || instance.State != db.InstanceStateDone {
		t.Fatalf("instance=%+v err=%v", instance, err)
	}
}
