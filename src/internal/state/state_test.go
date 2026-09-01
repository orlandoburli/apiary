package state

import "testing"

// TestNormalize_EveryLegacyVocabulary pins the mapping for every state string
// the four historical vocabularies could write. If a layer ever emits a value
// not listed here, Normalize returns it unchanged and this table is the place
// the omission should become visible.
func TestNormalize_EveryLegacyVocabulary(t *testing.T) {
	cases := []struct {
		legacy string
		layer  string
		want   State
		reason Reason
	}{
		// queue.JobState
		{"queued", "job", Queued, ""},
		{"leased", "job", Running, ""},
		{"succeeded", "job", Done, ""},
		{"failed", "job", Failed, ""},
		{"canceled", "job", Canceled, ""},

		// model.TaskState
		{"registered", "task", Queued, ""},
		{"running", "task", Running, ""},
		{"approval_waiting", "task", Blocked, ReasonApproval},
		{"done", "task", Done, ""},
		{"failed", "task", Failed, ""},

		// db.InstanceState
		{"pending", "instance", Queued, ""},
		{"running", "instance", Running, ""},
		{"approval_waiting", "instance", Blocked, ReasonApproval},
		{"waiting", "instance", Blocked, ReasonCI},
		{"interrupted", "instance", Blocked, ReasonInterrupted},
		{"done", "instance", Done, ""},
		{"failed", "instance", Failed, ""},

		// db.StepState
		{"pending", "step", Queued, ""},
		{"running", "step", Running, ""},
		{"passed", "step", Done, ""},
		{"failed", "step", Failed, ""},
		{"skipped", "step", Skipped, ""},
		{"skipped_cached", "step", Skipped, ReasonCached},
		{"interrupted", "step", Blocked, ReasonInterrupted},

		// Legacy execution status used by the dashboard's Agents tab.
		{"success", "execution", Done, ""},
	}

	for _, c := range cases {
		got, reason := NormalizeWithReason(c.legacy)
		if got != c.want {
			t.Errorf("NormalizeWithReason(%q) [%s] state = %q, want %q", c.legacy, c.layer, got, c.want)
		}
		if reason != c.reason {
			t.Errorf("NormalizeWithReason(%q) [%s] reason = %q, want %q", c.legacy, c.layer, reason, c.reason)
		}
		if plain := Normalize(c.legacy); plain != c.want {
			t.Errorf("Normalize(%q) [%s] = %q, want %q", c.legacy, c.layer, plain, c.want)
		}
	}
}

// TestNormalize_IsIdempotent guards the property the mixed-binary rollout
// depends on: a binary may normalize a value that some other binary already
// normalized, and must not change it a second time.
func TestNormalize_IsIdempotent(t *testing.T) {
	for _, s := range []State{Queued, Running, Blocked, Done, Failed, Canceled, Skipped} {
		if got := Normalize(string(s)); got != s {
			t.Errorf("Normalize(%q) = %q, want it unchanged", s, got)
		}
		if got := Normalize(string(Normalize(string(s)))); got != s {
			t.Errorf("Normalize twice on %q = %q, want it unchanged", s, got)
		}
	}
}

// TestNormalize_UnknownPassesThrough documents that Normalize never invents a
// state. An unrecognised value must reach the UI intact rather than being
// absorbed into a plausible-looking bucket, which would hide the bug.
func TestNormalize_UnknownPassesThrough(t *testing.T) {
	got, reason := NormalizeWithReason("something_new")
	if got != State("something_new") {
		t.Errorf("Normalize(unknown) = %q, want it returned unchanged", got)
	}
	if reason != "" {
		t.Errorf("Normalize(unknown) reason = %q, want empty", reason)
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []State{Done, Failed, Canceled, Skipped}
	live := []State{Queued, Running, Blocked}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%q.IsTerminal() = false, want true", s)
		}
	}
	for _, s := range live {
		if s.IsTerminal() {
			t.Errorf("%q.IsTerminal() = true, want false", s)
		}
	}
}

// TestIsTerminal_RetryIsNotFailed pins the distinction that makes Failed worth
// trusting: a task with attempts remaining is Blocked, not Failed, so Failed
// always means "no further attempts will happen".
func TestIsTerminal_RetryIsNotFailed(t *testing.T) {
	if Blocked.IsTerminal() {
		t.Fatal("Blocked must not be terminal: retry-pending work lives there")
	}
	if !Failed.IsTerminal() {
		t.Fatal("Failed must be terminal, or no consumer can trust it")
	}
}
