package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

func TestRenderWorkflowSteps_CIPolls(t *testing.T) {
	base := time.Date(2026, 6, 8, 17, 40, 0, 0, time.UTC)
	inst := &WorkflowInstanceItem{
		ID:       "wf_ci",
		Workflow: "implementation",
		State:    "waiting",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
		},
		CIPolls: []CIPollItem{
			{StepID: "check-ci", Status: "pending", CheckedAt: base},
			{StepID: "check-ci", Status: "pending", CheckedAt: base.Add(time.Minute)},
			{StepID: "check-ci", Status: "failed", Detail: `{"build":"failure"}`, CheckedAt: base.Add(2 * time.Minute)},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "CI Polls (3)") {
		t.Errorf("expected CI poll count header; got:\n%s", out)
	}
	if !strings.Contains(out, "last: failed") {
		t.Errorf("expected latest poll status in header; got:\n%s", out)
	}
	if !strings.Contains(out, `{"build":"failure"}`) {
		t.Errorf("expected per-check detail in a poll row; got:\n%s", out)
	}
}

func TestRenderWorkflowSteps_NoCIPolls(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_plain",
		Workflow: "implementation",
		State:    "done",
		Steps:    []WorkflowStepItem{{StepID: "implement", Agent: "engineer", State: "passed"}},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if strings.Contains(out, "CI Polls") {
		t.Errorf("instance with no polls should not render a CI Polls section:\n%s", out)
	}
}

func TestRenderWorkflowSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_1",
		Workflow: "feature-development",
		State:    "running",
		Steps: []WorkflowStepItem{
			{StepID: "plan", Agent: "architect", State: "passed", Duration: "12s"},
			{StepID: "implement", Agent: "backend-dev", State: "running", Duration: "3s"},
			{StepID: "review", Agent: "reviewer", State: "pending", Duration: "—"},
			{StepID: "cached-step", Agent: "architect", State: "passed", Duration: "5s", Cached: true},
		},
	}

	out := stripANSI(renderWorkflowSteps(inst, 80))

	for _, want := range []string{"feature-development", "running", "Steps", "plan", "implement", "review", "(cached)"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered steps missing %q:\n%s", want, out)
		}
	}
}

func TestRenderWorkflowSteps_ApprovalBanner(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_2",
		Workflow: "feature-development",
		State:    "approval_waiting", // legacy value: reason encoded in the state
		Message:  "Awaiting human approval — reply on the task to resume or abort.",
		Steps: []WorkflowStepItem{
			{StepID: "gate", Agent: "", State: "running", Duration: "—"},
		},
	}

	out := stripANSI(renderWorkflowSteps(inst, 80))
	// A legacy 'approval_waiting' row still reports what it is parked on, with
	// the reason recovered from the state name (#465).
	if !strings.Contains(out, "blocked:approval") {
		t.Errorf("expected blocked:approval badge:\n%s", out)
	}
	if !strings.Contains(out, "Awaiting human approval") {
		t.Errorf("expected approval message banner:\n%s", out)
	}
}

func TestRenderWorkflowSteps_ApprovalActions(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf-1", Workflow: "release", State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval), Message: "Awaiting approval", Approval: &db.ApprovalRequest{ID: "wf-1:gate", Approvers: []string{"alice", "carol"}, RequiredApprovals: 2}}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	for _, want := range []string{"alice, carol", "Press y to approve or n to reject"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	// With declared fields the answer is a form, not a keystroke — the detail
	// panel points at it rather than sending the operator to the webhook.
	inst.Approval.Fields = []map[string]any{{"name": "ticket", "required": true}}
	out = stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "Press a to answer (1 fields)") {
		t.Fatalf("structured-field guidance missing:\n%s", out)
	}
}

// A gate with no approvers is answered by whoever is at the keyboard, so the
// panel must not print an empty "Approvers:" line.
func TestRenderWorkflowSteps_OperatorGateOmitsApprovers(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf-2", Workflow: "release", State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
		Message: "Ship it?", Approval: &db.ApprovalRequest{ID: "wf-2:gate"}}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if strings.Contains(out, "Approvers:") {
		t.Fatalf("unexpected approvers line for an operator gate:\n%s", out)
	}
	if !strings.Contains(out, "Press y to approve or n to reject") {
		t.Fatalf("missing approve/reject hint:\n%s", out)
	}
}

func TestRenderWorkflowSteps_NoSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf_3", Workflow: "single", State: "done"}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "single") || !strings.Contains(out, "done") {
		t.Errorf("expected workflow id and state even with no steps:\n%s", out)
	}
	if strings.Contains(out, "Steps") {
		t.Errorf("should not render a Steps header when there are no steps:\n%s", out)
	}
}

// A failed instance whose recorded steps all passed (the run died in a later
// step that never persisted a step_run) must not read as a completed run — a
// marker should call out where it failed.
func TestRenderWorkflowSteps_FailedAfterPassedSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_f1",
		Workflow: "implementation",
		State:    "failed",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "no further steps recorded") {
		t.Errorf("expected a failure marker for an all-passed failed instance:\n%s", out)
	}
	if !strings.Contains(out, "after step 'implement'") {
		t.Errorf("marker should name the last recorded step:\n%s", out)
	}
}

// When the failing step is itself recorded, the list already shows it — no
// redundant marker.
func TestRenderWorkflowSteps_FailedStepVisible(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_f2",
		Workflow: "implementation",
		State:    "failed",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "30s"},
			{StepID: "review", Agent: "reviewer", State: "failed", Duration: "12s"},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if strings.Contains(out, "no further steps recorded") {
		t.Errorf("should not add a marker when a failed step is already visible:\n%s", out)
	}
}

// A failed instance with zero recorded steps still gets a marker.
func TestRenderWorkflowSteps_FailedNoSteps(t *testing.T) {
	inst := &WorkflowInstanceItem{ID: "wf_f3", Workflow: "implementation", State: "failed"}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "before any step completed") {
		t.Errorf("expected a failure marker for a failed instance with no steps:\n%s", out)
	}
}

// The task header span/usage must reflect the whole run: earliest step start,
// latest step finish, and tokens/cost summed across every instance — not just the
// last execution row (which would show only the final merge step).
func TestTaskRollup_SpansAllInstances(t *testing.T) {
	at := func(s string) *time.Time { v, _ := time.Parse(time.RFC3339, s); return &v }
	d := &TaskItem{
		// Execution-row values reflect only the last step (the merge): wrong for the header.
		StartedAt: at("2026-06-08T15:46:53Z"), CompletedAt: at("2026-06-08T15:47:35Z"),
		InputTokens: 0, OutputTokens: 0, TotalTokens: 0, CostUSD: 0.1312,
		Instances: []WorkflowInstanceItem{{
			StartedAt: at("2026-06-08T13:42:01Z"), FinishedAt: at("2026-06-08T15:47:35Z"),
			InputTokens: 90000, OutputTokens: 34533, TotalTokens: 124533, CostUSD: 0.95,
			CacheCreationTokens: 7000, CacheReadTokens: 60000,
		}},
	}
	start, end, in, out, total, cacheCreate, cacheRead, cost := taskRollup(d, nil)
	if start == nil || !start.Equal(*at("2026-06-08T13:42:01Z")) {
		t.Errorf("rollup start should be the first workflow start, got %v", start)
	}
	if end == nil || !end.Equal(*at("2026-06-08T15:47:35Z")) {
		t.Errorf("rollup end should be the last step finish, got %v", end)
	}
	if in != 90000 || out != 34533 || total != 124533 {
		t.Errorf("rollup tokens should sum instances, got %d/%d/%d", in, out, total)
	}
	if cacheCreate != 7000 || cacheRead != 60000 {
		t.Errorf("rollup cache tokens should sum instances, got %d write / %d read", cacheCreate, cacheRead)
	}
	if cost != 0.95 {
		t.Errorf("rollup cost should sum instances, got %v", cost)
	}
}

// With no instance-level usage the rollup falls back to the execution-row values
// so legacy single-shot tasks still report a span and spend.
func TestTaskRollup_FallsBackToExecutionRow(t *testing.T) {
	at := func(s string) *time.Time { v, _ := time.Parse(time.RFC3339, s); return &v }
	d := &TaskItem{
		StartedAt: at("2026-06-08T10:00:00Z"), CompletedAt: at("2026-06-08T10:05:00Z"),
		InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CostUSD: 0.01,
	}
	start, end, in, out, total, _, _, cost := taskRollup(d, nil)
	if start == nil || end == nil || in != 10 || out != 20 || total != 30 || cost != 0.01 {
		t.Errorf("expected execution-row fallback, got start=%v end=%v %d/%d/%d $%v", start, end, in, out, total, cost)
	}
}

// Each step row carries its started/ended timestamps (with date) and token count
// under a column header; the workflow header carries the dated rollup line.
func TestRenderWorkflowSteps_StepSpanAndTokens(t *testing.T) {
	at := func(s string) *time.Time { v, _ := time.Parse("2006-01-02 15:04:05", s); return &v }
	inst := &WorkflowInstanceItem{
		ID: "wf_s", Workflow: "implementation", State: "done",
		StartedAt: at("2026-06-08 13:42:01"), FinishedAt: at("2026-06-08 13:52:11"), TotalTokens: 50300,
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed", Duration: "8m15s",
				StartedAt: at("2026-06-08 13:42:01"), FinishedAt: at("2026-06-08 13:50:16"), TotalTokens: 42100},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	for _, want := range []string{
		"STARTED", "ENDED", "DURATION", "TOKENS", "STATE", // column header
		"06-08 13:42:01", "06-08 13:50:16", "42k", // step row cells (compact: no decimal above 10 units)
		"06-08 13:42:01 → 06-08 13:52:11", "50k tokens", // instance rollup
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered steps missing %q:\n%s", want, out)
		}
	}
}

// A meaningful idle gap between a step's end and the next step's start (CI waits,
// approvals, queue) is shown as a "waiting" connector; a short dispatch-latency
// gap below minStepWait is not.
func TestRenderWorkflowSteps_WaitBetweenSteps(t *testing.T) {
	at := func(s string) *time.Time { v, _ := time.Parse("2006-01-02 15:04:05", s); return &v }
	inst := &WorkflowInstanceItem{
		ID: "wf_w", Workflow: "implementation", State: "running",
		Steps: []WorkflowStepItem{
			{StepID: "implement", Agent: "engineer", State: "passed",
				StartedAt: at("2026-06-08 13:42:01"), FinishedAt: at("2026-06-08 13:50:16")},
			{StepID: "review", Agent: "reviewer", State: "passed", // 4s gap (< minStepWait) → no connector
				StartedAt: at("2026-06-08 13:50:20"), FinishedAt: at("2026-06-08 13:52:11")},
			{StepID: "merge", Agent: "engineer", State: "running", // ~2h CI/approval wait
				StartedAt: at("2026-06-08 15:46:53")},
		},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if !strings.Contains(out, "↓ 1h54m42s waiting") {
		t.Errorf("expected the long inter-step wait to be shown:\n%s", out)
	}
	if strings.Count(out, "waiting") != 1 {
		t.Errorf("a short sub-threshold gap should not render a wait connector; got %d:\n%s", strings.Count(out, "waiting"), out)
	}
}

// A non-failed instance never gets the marker.
func TestRenderWorkflowSteps_NoMarkerWhenDone(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID:       "wf_ok",
		Workflow: "implementation",
		State:    "done",
		Steps:    []WorkflowStepItem{{StepID: "implement", Agent: "engineer", State: "passed"}},
	}
	out := stripANSI(renderWorkflowSteps(inst, 80))
	if strings.Contains(out, "workflow failed") {
		t.Errorf("done instance should not show a failure marker:\n%s", out)
	}
}

// The parked-gate banner carries the step's own question, which is markdown. It
// used to be printed raw and unwrapped, so the markup showed as text and long
// lines spilled past the panel border.
func TestApprovalBannerRendersMarkdownWithinWidth(t *testing.T) {
	inst := &WorkflowInstanceItem{
		ID: "wf-1", Workflow: "release", State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
		Message: "Release 2.4 is staged.\n\n- migrations applied\n- " +
			strings.Repeat("a very long line that must be wrapped rather than spilling past the border ", 3),
		Approval: &db.ApprovalRequest{ID: "wf-1:gate"},
	}
	const width = 60

	out := renderWorkflowSteps(inst, width)
	for _, ln := range strings.Split(stripANSI(out), "\n") {
		if lipgloss.Width(ln) > width {
			t.Fatalf("line is %d wide, wider than the %d-column panel: %q", lipgloss.Width(ln), width, ln)
		}
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "migrations applied") {
		t.Fatalf("banner lost the message:\n%s", plain)
	}
	if strings.Contains(plain, "- migrations applied") {
		t.Fatalf("banner shows raw markdown instead of rendering it:\n%s", plain)
	}
}
