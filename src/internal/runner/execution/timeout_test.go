package execution

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// shellRunner builds a CliRunner that executes a shell snippet, so timeout
// behaviour can be exercised against a real subprocess rather than a fake.
func shellRunner(t *testing.T, script string) *CliRunner {
	t.Helper()
	r := &CliRunner{}
	if err := r.Configure(map[string]any{
		"command":      "/bin/sh",
		"args":         []any{"-c", script},
		"args_replace": true,
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return r
}

// A killed process reports only "signal: killed", so without explicit handling
// a timeout, a stall and a crash are indistinguishable in the run history —
// and they call for opposite responses.
func TestRunReportsTimeoutDistinctly(t *testing.T) {
	r := shellRunner(t, "sleep 30")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, _ := r.Run(ctx, model.RunRequest{Timeout: 300 * time.Millisecond})
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("the run was not cut off: took %v", elapsed)
	}
	if !res.TimedOut {
		t.Error("a run cut off at its deadline must be marked TimedOut")
	}
	if res.Success {
		t.Error("a timed-out run is not a success")
	}
	if res.Error == nil {
		t.Fatal("want an error explaining the timeout")
	}
	msg := res.Error.Error()
	if !strings.Contains(msg, "run timeout") || !strings.Contains(msg, "task_timeout") {
		t.Errorf("the error should name the bound that fired and the setting behind it, got: %s", msg)
	}
	if strings.Contains(msg, "signal: killed") {
		t.Errorf("a bare kill signal is not a diagnosis, got: %s", msg)
	}
	if res.FailureKind != model.FailureAborted {
		t.Errorf("FailureKind = %v, want aborted", res.FailureKind)
	}
}

// A step streaming tool calls for ninety minutes is working; one silent for
// twenty is usually wedged. The stall bound measures silence, not duration.
func TestStallTimeoutKillsASilentRun(t *testing.T) {
	r := shellRunner(t, "sleep 30")

	start := time.Now()
	res, _ := r.Run(context.Background(), model.RunRequest{
		Timeout:      time.Minute,
		StallTimeout: 400 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("the stall watchdog did not fire: took %v", elapsed)
	}
	if !res.TimedOut {
		t.Error("a stalled run must be marked TimedOut")
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "no output") {
		t.Errorf("the error should say the run went silent, got: %v", res.Error)
	}
	if res.Error != nil && !strings.Contains(res.Error.Error(), "stall_timeout") {
		t.Errorf("the error should name the setting that fired, got: %v", res.Error)
	}
}

// The whole point of measuring silence rather than duration: a run that keeps
// producing output must survive well past its stall bound.
func TestStallTimeoutSparesAChattyRun(t *testing.T) {
	// Emits a line every 100ms for ~1.2s, with a 400ms stall bound. Total
	// runtime far exceeds the bound; the gaps never do.
	r := shellRunner(t, "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do echo line$i; sleep 0.1; done")

	res, err := r.Run(context.Background(), model.RunRequest{
		Timeout:      time.Minute,
		StallTimeout: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.TimedOut {
		t.Error("a run that keeps producing output must not be killed as stalled")
	}
	if !res.Success {
		t.Errorf("want success, got error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "line12") {
		t.Errorf("the run should have completed, output: %q", res.Output)
	}
}

// An agent inside ask_human emits nothing while it waits, so the silence the
// watchdog sees is a person taking their time, not a wedged process. The clock
// restarts while a question is pending; once it is resolved the silence counts
// again and the run is killed like any other.
func TestStallTimeoutSparesARunWaitingOnHuman(t *testing.T) {
	r := shellRunner(t, "sleep 30")

	var waiting atomic.Bool
	waiting.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resCh := make(chan model.RunResult, 1)
	go func() {
		res, _ := r.Run(ctx, model.RunRequest{
			Timeout:        time.Minute,
			StallTimeout:   300 * time.Millisecond,
			WaitingOnHuman: waiting.Load,
		})
		resCh <- res
	}()

	// Several stall bounds pass with no output at all: the child must survive
	// because the question is still pending.
	select {
	case res := <-resCh:
		t.Fatalf("the run was killed while waiting on a human: %+v", res)
	case <-time.After(1200 * time.Millisecond):
	}

	// The question is gone (answered or expired) and the child stays silent.
	waiting.Store(false)
	select {
	case res := <-resCh:
		if !res.TimedOut {
			t.Error("a silent run after the question resolved must be marked TimedOut")
		}
		if res.Error == nil || !strings.Contains(res.Error.Error(), "no output") {
			t.Errorf("want the stall diagnosis, got: %v", res.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stall watchdog never fired once the question was resolved")
	}
}

func TestStallTimeoutDisabledByDefault(t *testing.T) {
	// No StallTimeout: a silent run is bounded only by the context.
	r := shellRunner(t, "sleep 0.5; echo done")

	res, err := r.Run(context.Background(), model.RunRequest{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.TimedOut {
		t.Error("with no stall timeout configured, a quiet run must not be killed")
	}
	if !strings.Contains(res.Output, "done") {
		t.Errorf("output = %q, want the run to complete", res.Output)
	}
}

// A normal non-zero exit must keep its diagnosable error, not be relabelled as
// a timeout.
func TestOrdinaryFailureIsNotReportedAsATimeout(t *testing.T) {
	r := shellRunner(t, "echo 'something broke' >&2; exit 3")

	res, _ := r.Run(context.Background(), model.RunRequest{Timeout: time.Minute})
	if res.TimedOut {
		t.Error("an ordinary non-zero exit is not a timeout")
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "something broke") {
		t.Errorf("the stderr detail should survive, got: %v", res.Error)
	}
}
