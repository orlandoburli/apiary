package dashboard

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTaskNavigationLoadingBlocksActions(t *testing.T) {
	for _, key := range []string{"d", "enter", "l"} {
		t.Run(key, func(t *testing.T) {
			a := newTestApp(100, 30)
			a.model.SetActiveTab("Tasks")
			a.model.tasksTab.History = []TaskItem{{TaskID: "task-1", Title: "A task", Status: "running"}}
			_, cmd := a.Update(keyPress(key))
			if cmd == nil || a.navigationLabel == "" {
				t.Fatal("task navigation must start a loading screen")
			}
			if view := stripANSI(a.View()); !strings.Contains(view, "LOADING") || !strings.Contains(view, "Esc: cancel") {
				t.Fatalf("missing loading feedback: %s", view)
			}
			for _, repeat := range []string{"d", "enter", "l", "r", "down", "tab", "R", "W"} {
				if _, next := a.Update(keyPress(repeat)); next != nil {
					t.Fatalf("%s issued an action during loading", repeat)
				}
			}
			if a.model.tasksTab.SelectedIdx != 0 || a.model.ActiveTab() != "Tasks" {
				t.Fatal("navigation changed while loading")
			}
			if a.fetchActiveTab() != nil {
				t.Fatal("background refresh should pause during navigation")
			}
			// An unrelated background message must not unlock the transition.
			a.Update(workflowsConfigMsg{})
			if a.navigationLabel == "" {
				t.Fatal("unrelated result cleared loading screen")
			}
			a.Update(spinnerTickMsg(time.Now()))
			if a.model.spinnerFrame == 0 {
				t.Fatal("spinner did not advance")
			}
		})
	}
}

func TestTaskNavigationCompletionAndCancellation(t *testing.T) {
	a := newTestApp(100, 30)
	a.model.SetActiveTab("Tasks")
	detail := &TaskItem{TaskID: "task-1", Title: "Loaded task"}
	old := a.beginNavigation("Loading task details…", func() tea.Msg { return taskDetailMsg{detail: detail} })
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.navigationLabel != "" {
		t.Fatal("escape did not dismiss loading")
	}
	a.Update(old())
	if a.model.tasksTab.View != TaskViewList {
		t.Fatal("cancelled result opened details")
	}
	next := a.beginNavigation("Loading task workflows…", func() tea.Msg {
		return workflowMonitorMsg{taskID: "task-2", instances: []*WorkflowInstanceItem{{ID: "instance-2"}}}
	})
	a.Update(old())
	if a.navigationLabel == "" {
		t.Fatal("old result dismissed a newer loading screen")
	}
	a.Update(next())
	if a.navigationLabel != "" || a.model.tasksTab.View != TaskViewWorkflow {
		t.Fatal("workflow completion did not open the monitor")
	}
	if a.model.tasksTab.WorkflowTaskID != "task-2" {
		t.Fatal("opened the wrong task")
	}
	a.Update(old())
	if a.model.tasksTab.View != TaskViewWorkflow {
		t.Fatal("late details replaced the workflow")
	}
}

func TestTaskNavigationDetailAndMissingResult(t *testing.T) {
	for _, missing := range []bool{false, true} {
		a := newTestApp(100, 30)
		a.model.SetActiveTab("Tasks")
		a.model.tasksTab.History = []TaskItem{{TaskID: "task-1", Title: "A task"}}
		var detail *TaskItem
		if !missing {
			detail = &TaskItem{TaskID: "task-1", Title: "A task"}
		}
		cmd := a.beginNavigation("Loading task details…", func() tea.Msg { return taskDetailMsg{detail: detail} })
		a.Update(cmd())
		if a.navigationLabel != "" || a.model.loading {
			t.Fatal("loading did not clear on completion")
		}
		if missing {
			if a.model.tasksTab.View != TaskViewList || !a.model.noticeIsErr {
				t.Fatal("missing task must return to previous screen with feedback")
			}
		} else if a.model.tasksTab.View != TaskViewDetail || a.model.tasksTab.Detail != detail {
			t.Fatal("details were not opened")
		}
	}
}

func TestTaskNavigationQuit(t *testing.T) {
	for _, key := range []tea.KeyMsg{keyPress("q"), {Type: tea.KeyCtrlC}} {
		a := newTestApp(100, 30)
		a.beginNavigation("Loading…", func() tea.Msg { return nil })
		_, cmd := a.Update(key)
		if cmd == nil {
			t.Fatal("quit unavailable during loading")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("expected quit command")
		}
	}
}
