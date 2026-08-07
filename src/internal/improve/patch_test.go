package improve

import (
	"strings"
	"testing"
)

func TestParsePatchAcceptsAWellFormedDiff(t *testing.T) {
	diff := `--- a/apiary.yaml
+++ b/apiary.yaml
@@ -2,3 +2,4 @@ agents:
   - id: engineer
     model: sonnet
+    max_turns: 40
     max_workers: 5
`
	p, err := ParsePatch(diff)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if p.Path != "apiary.yaml" {
		t.Errorf("Path = %q, want apiary.yaml (a/ and b/ prefixes stripped)", p.Path)
	}
	if len(p.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(p.Hunks))
	}
	h := p.Hunks[0]
	if h.OldStart != 2 || h.OldLines != 3 || h.NewStart != 2 || h.NewLines != 4 {
		t.Errorf("hunk range = -%d,%d +%d,%d; want -2,3 +2,4", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
	if added, removed := p.Stats(); added != 1 || removed != 0 {
		t.Errorf("Stats = +%d −%d, want +1 −0", added, removed)
	}
}

// An advisor emitted `@@ implementation/merge step @@` on the first real run —
// naming the section in prose instead of giving line numbers. Guessing where
// that belongs is how a patch lands in the wrong place, so it must fail.
func TestParsePatchRejectsProseHunkHeaders(t *testing.T) {
	diff := `--- a/apiary.yaml
+++ b/apiary.yaml
@@ implementation/merge step @@
       - id: merge
-        agent: engineer
+        agent: engineer-merge
`
	_, err := ParsePatch(diff)
	if err == nil {
		t.Fatal("a hunk header without line numbers must be rejected")
	}
	if !strings.Contains(err.Error(), "hunk header") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

func TestParsePatchRejectsMalformedInput(t *testing.T) {
	cases := []struct{ name, diff string }{
		{"no target header", "@@ -1,1 +1,1 @@\n-a\n+b\n"},
		{"no hunks", "--- a/x.yaml\n+++ b/x.yaml\n"},
		{"missing closing @@", "--- a/x\n+++ b/x\n@@ -1,1 +1,1\n-a\n+b\n"},
		{"non-numeric range", "--- a/x\n+++ b/x\n@@ -a,b +c,d @@\n-a\n+b\n"},
		{"dev null target", "--- a/x\n+++ /dev/null\n@@ -1,1 +1,1 @@\n-a\n"},
		{"wrong range sign", "--- a/x\n+++ b/x\n@@ +1,1 -1,1 @@\n-a\n+b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePatch(tc.diff); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestApplyAddsRemovesAndKeepsContext(t *testing.T) {
	original := "line1\nline2\nline3\nline4\n"
	diff := `--- a/f
+++ b/f
@@ -1,4 +1,4 @@
 line1
-line2
+line2-changed
 line3
 line4
`
	p, err := ParsePatch(diff)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	got, err := p.Apply(original)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "line1\nline2-changed\nline3\nline4\n"
	if got != want {
		t.Errorf("Apply =\n%q\nwant\n%q", got, want)
	}
}

// Context that does not match must fail rather than search nearby. A patch that
// lands in the wrong place is worse than one that does not land, because the
// diff shown to the reviewer would no longer describe what the file became.
func TestApplyRefusesToFuzzyMatch(t *testing.T) {
	original := "alpha\nbeta\ngamma\n"
	diff := `--- a/f
+++ b/f
@@ -1,3 +1,3 @@
 alpha
-BETA
+delta
 gamma
`
	p, _ := ParsePatch(diff)
	_, err := p.Apply(original)
	if err == nil {
		t.Fatal("a context mismatch must be an error")
	}
	if !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "actual") {
		t.Errorf("error should show both sides, got: %v", err)
	}
}

func TestApplyMultipleHunks(t *testing.T) {
	original := "a\nb\nc\nd\ne\nf\n"
	diff := `--- a/f
+++ b/f
@@ -1,2 +1,3 @@
 a
+new-after-a
 b
@@ -5,2 +6,2 @@
-e
+E
 f
`
	p, err := ParsePatch(diff)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	got, err := p.Apply(original)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "a\nnew-after-a\nb\nc\nd\nE\nf\n" {
		t.Errorf("Apply = %q", got)
	}
}

func TestApplyRejectsOutOfOrderHunks(t *testing.T) {
	original := "a\nb\nc\nd\n"
	diff := `--- a/f
+++ b/f
@@ -3,1 +3,1 @@
-c
+C
@@ -1,1 +1,1 @@
-a
+A
`
	p, _ := ParsePatch(diff)
	if _, err := p.Apply(original); err == nil {
		t.Fatal("hunks that go backwards must be rejected")
	}
}

func TestApplyRejectsHunkPastEndOfFile(t *testing.T) {
	p, _ := ParsePatch("--- a/f\n+++ b/f\n@@ -99,1 +99,1 @@\n-x\n+y\n")
	if _, err := p.Apply("a\nb\n"); err == nil {
		t.Fatal("a hunk starting past the end of the file must be rejected")
	}
}

func TestApplyPreservesTrailingNewlineState(t *testing.T) {
	diff := `--- a/f
+++ b/f
@@ -1,2 +1,2 @@
 a
-b
+B
`
	p, _ := ParsePatch(diff)

	withNL, err := p.Apply("a\nb\n")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if withNL != "a\nB\n" {
		t.Errorf("with trailing newline: got %q", withNL)
	}

	// Re-adding a newline that was not there is a spurious change.
	withoutNL, err := p.Apply("a\nb")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if withoutNL != "a\nB" {
		t.Errorf("without trailing newline: got %q, want no trailing newline added", withoutNL)
	}
}

func TestParsePatchToleratesSurroundingProse(t *testing.T) {
	// Models routinely wrap a diff in explanation. The diff itself is what matters.
	diff := "Here is the change I propose:\n\n" + `--- a/f
+++ b/f
@@ -1,1 +1,1 @@
-a
+b
` + "\nThat should do it.\n"
	p, err := ParsePatch(diff)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if got, err := p.Apply("a\n"); err != nil || got != "b\n" {
		t.Errorf("Apply = %q, %v", got, err)
	}
}
