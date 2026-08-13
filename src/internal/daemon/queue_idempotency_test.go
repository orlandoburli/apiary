package daemon

import "testing"

// The automatic paths must keep the exact key they had before the suffix existed,
// or every job already in a live queue stops de-duplicating against re-polls.
func TestDispatchIdempotencyKey_UnsuffixedIsUnchanged(t *testing.T) {
	if got, want := dispatchIdempotencyKey("task-1", 3, "triage", ""), "task-1:3:triage"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

// A suffix widens the key, so a caller that means to repeat a dispatch is not
// de-duplicated against the round already queued for the same task+workflow.
func TestDispatchIdempotencyKey_SuffixWidensTheKey(t *testing.T) {
	plain := dispatchIdempotencyKey("task-1", 3, "triage", "")
	suffixed := dispatchIdempotencyKey("task-1", 3, "triage", "manual-ab")
	if suffixed == plain {
		t.Fatalf("suffixed key %q did not differ from %q", suffixed, plain)
	}
	if suffixed != "task-1:3:triage:manual-ab" {
		t.Errorf("suffixed key = %q", suffixed)
	}
}
