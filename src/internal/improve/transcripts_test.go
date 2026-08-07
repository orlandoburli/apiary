package improve

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A task's transcript directory holds one file per step. Selecting by
// modification time returns whichever step ran LAST — almost always the final
// step of the workflow — so every hotspot receives the same terminal transcript
// no matter which step is being sampled. That silently substitutes the evidence
// the whole analysis rests on.
//
// Observed against a real database: sampling implement, validate, review and
// triage all returned the same 4618-byte "merge" transcript, while the 103KB
// implement transcript that would have explained the rework was never read. The
// advisor noticed before the tests did — it reported "root cause not visible in
// provided transcripts, which cover only merge steps".
func TestTranscriptForStepMatchesTheStepNotTheNewestFile(t *testing.T) {
	dir := t.TempDir()

	// Real naming: <instance>_<n>-<stepID>.md, written in execution order so
	// merge is the newest.
	files := []struct {
		name string
		age  time.Duration
	}{
		{"wf_1786129309127914000_190-implement.md", 20 * time.Minute},
		{"wf_1786129309127914000_190-validate.md", 10 * time.Minute},
		{"wf_1786129309127914000_190-review.md", 5 * time.Minute},
		{"wf_1786129309127914000_190-merge.md", 1 * time.Minute},
	}
	now := time.Now()
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte("# "+f.name), 0o600); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(-f.age)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	for _, step := range []string{"implement", "validate", "review", "merge"} {
		got := transcriptForStep(dir, step)
		want := "wf_1786129309127914000_190-" + step + ".md"
		if got != want {
			t.Errorf("transcriptForStep(%q) = %q, want %q", step, got, want)
		}
	}
}

// A rework loop writes several transcripts for the same step. The most recent is
// the attempt whose outcome the metrics describe.
func TestTranscriptForStepPrefersTheLatestRunOfThatStep(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	for i, f := range []struct {
		name string
		age  time.Duration
	}{
		{"wf_1_1-implement.md", 30 * time.Minute},
		{"wf_1_2-implement.md", 20 * time.Minute}, // the retry
		{"wf_1_3-merge.md", 1 * time.Minute},      // newer, but a different step
	} {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte{byte(i)}, 0o600); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(-f.age)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	if got := transcriptForStep(dir, "implement"); got != "wf_1_2-implement.md" {
		t.Errorf("transcriptForStep = %q, want the latest implement run", got)
	}
}

func TestTranscriptForStepMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wf_1_1-merge.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := transcriptForStep(dir, "implement"); got != "" {
		t.Errorf("transcriptForStep = %q, want empty when the step has no transcript", got)
	}
	if got := transcriptForStep(filepath.Join(dir, "nope"), "implement"); got != "" {
		t.Errorf("transcriptForStep = %q, want empty for a missing directory", got)
	}
}

// A step id that is a suffix of another must not match it: "validate" and
// "revalidate" are different steps.
func TestTranscriptForStepDoesNotMatchSuffixCollisions(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"wf_1_1-revalidate.md", "wf_1_2-validate.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := transcriptForStep(dir, "validate"); got != "wf_1_2-validate.md" {
		t.Errorf("transcriptForStep = %q, want the exact step match", got)
	}
}
