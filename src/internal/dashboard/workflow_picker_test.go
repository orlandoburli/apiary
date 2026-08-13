package dashboard

import (
	"strings"
	"testing"
)

func pickerApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(100, 30)
	a.model.workflowsTab = &WorkflowsTab{Workflows: []WorkflowConfigItem{
		{ID: "triage"}, {ID: "implement"}, {ID: "nightly-audit"},
	}}
	return a
}

func TestWorkflowPicker_OpensOnW(t *testing.T) {
	a := pickerApp(t)

	a.handleKeyMsg(keyPress("W"))
	if !a.model.pickerActive {
		t.Fatal("W did not open the workflow picker")
	}

	out := stripANSI(a.renderWorkflowPicker(""))
	for _, id := range []string{"triage", "implement", "nightly-audit"} {
		if !strings.Contains(out, id) {
			t.Errorf("picker does not list %q; got:\n%s", id, out)
		}
	}
	// With nothing focused the run has no item, and the header must say so
	// before the user commits to it.
	if !strings.Contains(out, "standalone") {
		t.Errorf("picker does not name the standalone target; got:\n%s", out)
	}
}

func TestWorkflowPicker_NavigatesAndCancels(t *testing.T) {
	a := pickerApp(t)
	a.handleKeyMsg(keyPress("W"))

	a.handleKeyMsg(keyPress("down"))
	a.handleKeyMsg(keyPress("down"))
	if a.model.pickerIdx != 2 {
		t.Errorf("pickerIdx = %d after two downs, want 2", a.model.pickerIdx)
	}
	// The selection stops at the end rather than wrapping into an invalid index.
	a.handleKeyMsg(keyPress("down"))
	if a.model.pickerIdx != 2 {
		t.Errorf("pickerIdx = %d past the last row, want 2", a.model.pickerIdx)
	}
	a.handleKeyMsg(keyPress("up"))
	if a.model.pickerIdx != 1 {
		t.Errorf("pickerIdx = %d after up, want 1", a.model.pickerIdx)
	}

	a.handleKeyMsg(keyPress("esc"))
	if a.model.pickerActive {
		t.Fatal("esc did not close the picker")
	}
}

// While the picker is open it owns every key, like the confirm modal — otherwise
// navigation keys move the list underneath the overlay.
func TestWorkflowPicker_SwallowsKeysWhileOpen(t *testing.T) {
	a := pickerApp(t)
	a.handleKeyMsg(keyPress("W"))

	a.handleKeyMsg(keyPress("R"))
	if a.model.confirmAction != "" {
		t.Errorf("R leaked past the open picker and armed a %q confirmation", a.model.confirmAction)
	}
	if !a.model.pickerActive {
		t.Error("picker closed on an unrelated key")
	}
}

// `s` detaches the run from the focused item, for a workflow that should not
// bind one.
func TestWorkflowPicker_TogglesStandalone(t *testing.T) {
	a := pickerApp(t)
	a.model.pickerActive = true
	a.model.pickerTaskID = "CDT-123"

	if ref, standalone := a.pickerTarget(); standalone || ref != "CDT-123" {
		t.Fatalf("target = (%q, standalone=%v), want the focused item", ref, standalone)
	}
	if !strings.Contains(stripANSI(a.renderWorkflowPicker("")), "item CDT-123") {
		t.Error("picker does not name the item it will bind")
	}

	a.handleKeyMsg(keyPress("s"))
	if ref, standalone := a.pickerTarget(); !standalone || ref != "" {
		t.Fatalf("target = (%q, standalone=%v) after `s`, want standalone", ref, standalone)
	}
	a.handleKeyMsg(keyPress("s"))
	if _, standalone := a.pickerTarget(); standalone {
		t.Error("`s` did not toggle back to the focused item")
	}
}

func TestWorkflowPicker_NoWorkflowsDoesNotOpen(t *testing.T) {
	a := newTestApp(100, 30) // no workflows tab, no config

	a.handleKeyMsg(keyPress("W"))
	if a.model.pickerActive {
		t.Fatal("picker opened with no workflows to pick from")
	}
}
