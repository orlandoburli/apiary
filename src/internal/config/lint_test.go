package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newRawConfig builds a Config carrying raw YAML so the lint checks (which key off
// rawContent) run, without going through file I/O or the rest of Validate.
func newRawConfig(raw string) *Config {
	return &Config{rawContent: raw}
}

func TestLint_RemovedAssignFromOutput(t *testing.T) {
	raw := `version: "1"
routes:
  - id: classify
    agent: investigator
    on_complete:
      assign_from_output: true
`
	errs := newRawConfig(raw).lint()
	if len(errs) == 0 {
		t.Fatal("expected an error for assign_from_output, got none")
	}
	got := errs[0].Error()
	if !strings.Contains(got, "assign_from_output") {
		t.Errorf("error should name the directive: %q", got)
	}
	if !strings.Contains(got, "triage") || !strings.Contains(got, "split") {
		t.Errorf("error should point to the triage/split replacement: %q", got)
	}
	if !strings.Contains(got, "line 6") {
		t.Errorf("error should cite the source line (6): %q", got)
	}
}

func TestLint_RemovedAssignLabelPrefix(t *testing.T) {
	raw := `version: "1"
routes:
  - id: classify
    agent: investigator
    on_complete:
      assign_label_prefix: "agent:"
`
	errs := newRawConfig(raw).lint()
	if len(errs) == 0 {
		t.Fatal("expected an error for assign_label_prefix, got none")
	}
	if !strings.Contains(errs[0].Error(), "assign_label_prefix") {
		t.Errorf("error should name the directive: %q", errs[0].Error())
	}
}

func TestLint_UnknownFieldTypo(t *testing.T) {
	// `lables` is a typo for `labels` inside a route match.
	raw := `version: "1"
routes:
  - id: r1
    agent: a1
    match:
      source: src
      lables: [bug]
`
	errs := newRawConfig(raw).lint()
	if len(errs) == 0 {
		t.Fatal("expected an unknown-field error for the `lables` typo, got none")
	}
	joined := joinErrs(errs)
	if !strings.Contains(joined, "lables") || !strings.Contains(joined, "unknown config field") {
		t.Errorf("expected an unknown-field error mentioning `lables`, got: %q", joined)
	}
}

func TestLint_CleanConfigNoErrors(t *testing.T) {
	raw := `version: "1"
agents:
  - id: a1
    model: claude-sonnet-4-6
routes:
  - id: r1
    priority: 1
    agent: a1
    match:
      source: src
    on_complete:
      set_state: done
`
	if errs := newRawConfig(raw).lint(); len(errs) != 0 {
		t.Fatalf("expected no lint errors, got: %v", errs)
	}
}

func TestLint_EmptyRawContentIsNoOp(t *testing.T) {
	// A Config built in code (no rawContent) must never trip the raw-text lints,
	// even if it holds a removed directive in its structs.
	c := &Config{
		Routes: []RouteConfig{
			{ID: "r1", OnComplete: OnComplete{AssignFromOutput: true}},
		},
	}
	if errs := c.lint(); errs != nil {
		t.Fatalf("expected nil for empty rawContent, got: %v", errs)
	}
}

// TestLint_ExampleConfigsNoFalsePositives strict-decodes and removed-checks every
// shipped example so a stricter validator never rejects a config we ship.
func TestLint_ExampleConfigsNoFalsePositives(t *testing.T) {
	root := repoRoot(t)
	examples := []string{
		".apiary/example-apiary-full.yaml",
		".apiary/example-workflow.yaml",
		".apiary/example-with-recovery.yaml",
		".apiary/apiary.yaml",
	}
	for _, rel := range examples {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Skipf("example not found: %v", err)
			}
			if errs := newRawConfig(string(data)).lint(); len(errs) != 0 {
				t.Errorf("%s should lint clean, got: %v", rel, errs)
			}
		})
	}
}

func joinErrs(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// repoRoot walks up from the test's working directory to the repository root (the
// dir holding the .apiary/ example configs), which sits above the Go module root
// (go.mod lives in src/), so example paths resolve regardless of package location.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if fi, err := os.Stat(filepath.Join(dir, ".apiary")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repository root (.apiary/)")
	return ""
}
