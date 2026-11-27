package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentQuestionCreateAndAnswer(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	q := &AgentQuestion{
		WorkflowInstanceID: "wf_1",
		StepID:             "review",
		TaskID:             "task_1",
		AgentID:            "coder",
		Question:           "Which branch should I target?",
		Options:            []QuestionOption{{Value: "main"}, {Value: "release"}},
		AllowFreeText:      true,
	}
	if err := c.CreateQuestion(ctx, q); err != nil {
		t.Fatalf("create: %v", err)
	}
	if q.ID == "" {
		t.Fatal("create did not assign an id")
	}

	stored, err := c.GetQuestion(ctx, q.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: %v %v", stored, err)
	}
	if stored.Status != QuestionPending || !stored.AllowFreeText || len(stored.Options) != 2 {
		t.Fatalf("stored=%+v", stored)
	}
	if stored.Question != q.Question || stored.WorkflowInstanceID != "wf_1" || stored.TaskID != "task_1" {
		t.Fatalf("round-trip lost fields: %+v", stored)
	}

	answered, won, err := c.AnswerQuestion(ctx, q.ID, "main", "ship it", "alice")
	if err != nil || !won {
		t.Fatalf("answer: won=%v err=%v", won, err)
	}
	if answered.Status != QuestionAnswered || answered.Answer != "main" || answered.AnswerReason != "ship it" || answered.AnsweredBy != "alice" || answered.AnsweredAt == nil {
		t.Fatalf("answered=%+v", answered)
	}
}

// Exactly one of several racing answers may win: the long-poll and a dashboard
// can both submit, and the loser must re-read instead of overwriting.
func TestAgentQuestionAnswerIsIdempotentUnderRace(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	q := &AgentQuestion{Question: "ok?", AllowFreeText: true}
	if err := c.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, won, err := c.AnswerQuestion(ctx, q.ID, "yes", "", "human")
			if err != nil {
				t.Errorf("answer: %v", err)
			}
			if won {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wins=%d, want 1", wins.Load())
	}
	stored, _ := c.GetQuestion(ctx, q.ID)
	if stored.Status != QuestionAnswered || stored.Answer != "yes" {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestAgentQuestionExpirySweep(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	past := time.Now().UTC().Add(-time.Minute)
	q := &AgentQuestion{Question: "still there?", AllowFreeText: true, ExpiresAt: &past}
	if err := c.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	n, err := c.ExpireQuestions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	stored, _ := c.GetQuestion(ctx, q.ID)
	if stored.Status != QuestionTimedOut {
		t.Fatalf("status=%s, want timed_out", stored.Status)
	}
	// Even without the sweep, an expired row cannot be answered: the conditional
	// UPDATE checks the deadline itself.
	if _, won, err := c.AnswerQuestion(ctx, q.ID, "late", "", "human"); err != nil || won {
		t.Fatalf("late answer: won=%v err=%v", won, err)
	}
}

// The runner asks this of every silent step: only a pending, unexpired question
// means the silence is a person thinking.
func TestAgentQuestionHasPendingForStep(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	q := &AgentQuestion{WorkflowInstanceID: "wf_1", StepID: "implement", Question: "which env?", AllowFreeText: true}
	if err := c.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	pending, err := c.HasPendingQuestion(ctx, "wf_1", "implement")
	if err != nil || !pending {
		t.Fatalf("pending=%v err=%v, want true", pending, err)
	}
	// Another step of the same instance is not waiting on anything.
	if pending, err := c.HasPendingQuestion(ctx, "wf_1", "review"); err != nil || pending {
		t.Fatalf("other step: pending=%v err=%v, want false", pending, err)
	}
	// Neither is a plain run with no workflow identity.
	if pending, err := c.HasPendingQuestion(ctx, "", ""); err != nil || pending {
		t.Fatalf("empty ids: pending=%v err=%v, want false", pending, err)
	}

	if _, _, err := c.AnswerQuestion(ctx, q.ID, "staging", "", "human"); err != nil {
		t.Fatal(err)
	}
	if pending, err := c.HasPendingQuestion(ctx, "wf_1", "implement"); err != nil || pending {
		t.Fatalf("answered step: pending=%v err=%v, want false", pending, err)
	}
}

func TestAgentQuestionHasPendingIgnoresExpired(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	past := time.Now().UTC().Add(-time.Minute)
	q := &AgentQuestion{WorkflowInstanceID: "wf_1", StepID: "implement", Question: "still there?", ExpiresAt: &past}
	if err := c.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	// Even before the sweeper flips the row, an expired deadline means the tool
	// is no longer waiting, so the stall clock must start again.
	if pending, err := c.HasPendingQuestion(ctx, "wf_1", "implement"); err != nil || pending {
		t.Fatalf("pending=%v err=%v, want false for an expired question", pending, err)
	}
}

// A finished run leaves nobody to answer, so its questions are closed with the
// existing timed_out status and stop appearing as pending.
func TestAgentQuestionExpireStepQuestions(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	mine := &AgentQuestion{WorkflowInstanceID: "wf_1", StepID: "implement", Question: "mine?", AllowFreeText: true}
	otherStep := &AgentQuestion{WorkflowInstanceID: "wf_1", StepID: "review", Question: "other step?", AllowFreeText: true}
	otherInstance := &AgentQuestion{WorkflowInstanceID: "wf_2", StepID: "implement", Question: "other instance?", AllowFreeText: true}
	answered := &AgentQuestion{WorkflowInstanceID: "wf_1", StepID: "implement", Question: "answered?", AllowFreeText: true}
	for _, q := range []*AgentQuestion{mine, otherStep, otherInstance, answered} {
		if err := c.CreateQuestion(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.AnswerQuestion(ctx, answered.ID, "yes", "", "human"); err != nil {
		t.Fatal(err)
	}

	n, err := c.ExpireStepQuestions(ctx, "wf_1", "implement")
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v, want 1", n, err)
	}
	if stored, _ := c.GetQuestion(ctx, mine.ID); stored.Status != QuestionTimedOut {
		t.Fatalf("mine status=%s, want timed_out", stored.Status)
	}
	if stored, _ := c.GetQuestion(ctx, answered.ID); stored.Status != QuestionAnswered || stored.Answer != "yes" {
		t.Fatalf("answered row was touched: %+v", stored)
	}
	for _, q := range []*AgentQuestion{otherStep, otherInstance} {
		if stored, _ := c.GetQuestion(ctx, q.ID); stored.Status != QuestionPending {
			t.Fatalf("%s status=%s, want pending", q.Question, stored.Status)
		}
	}
	// Nothing left to expire is a no-op, not an error.
	if n, err := c.ExpireStepQuestions(ctx, "wf_1", "implement"); err != nil || n != 0 {
		t.Fatalf("second expire: n=%d err=%v, want 0", n, err)
	}
	if n, err := c.ExpireStepQuestions(ctx, "", ""); err != nil || n != 0 {
		t.Fatalf("empty ids: n=%d err=%v, want 0", n, err)
	}
}

func TestAgentQuestionListPending(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	pending := &AgentQuestion{Question: "pending?", AllowFreeText: true}
	if err := c.CreateQuestion(ctx, pending); err != nil {
		t.Fatal(err)
	}
	answered := &AgentQuestion{Question: "answered?", AllowFreeText: true}
	if err := c.CreateQuestion(ctx, answered); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AnswerQuestion(ctx, answered.ID, "yes", "", "human"); err != nil {
		t.Fatal(err)
	}

	list, err := c.ListPendingQuestions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != pending.ID {
		t.Fatalf("pending list=%+v", list)
	}
	all, err := c.ListQuestions(ctx, "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("all list len=%d err=%v", len(all), err)
	}
}
