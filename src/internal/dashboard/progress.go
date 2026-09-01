package dashboard

import (
	"strconv"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

// taskProgress is how far a task has got: which step is executing, where that
// step sits in its workflow, and how many instances are live.
type taskProgress struct {
	StepID    string
	Position  int // 1-based; 0 when unknown
	Total     int // 0 when unknown
	Instances int // live instances; >1 means fan-out
}

// resolveTaskProgress folds the flat step rows of ListStepProgressForTasks into
// one progress value per task.
//
// The step reported for an instance is the one actually executing — the last
// row that is running or blocked. When every step has settled (the instance is
// between steps, or waiting to be settled), the most recent row is used instead,
// because "the step it just finished" is a better answer than nothing.
func resolveTaskProgress(rows []db.TaskStepRow, workflows []config.WorkflowConfig) map[string]taskProgress {
	type instAcc struct {
		workflowID string
		current    string // step chosen by the rule above
		lastSeen   string
	}

	// task -> instance -> accumulator, plus the instance order per task.
	acc := map[string]map[string]*instAcc{}
	order := map[string][]string{}

	for _, r := range rows {
		if r.TaskID == "" {
			continue
		}
		byInst, ok := acc[r.TaskID]
		if !ok {
			byInst = map[string]*instAcc{}
			acc[r.TaskID] = byInst
		}
		a, ok := byInst[r.InstanceID]
		if !ok {
			a = &instAcc{workflowID: r.WorkflowID}
			byInst[r.InstanceID] = a
			order[r.TaskID] = append(order[r.TaskID], r.InstanceID)
		}
		if r.StepID == "" {
			continue
		}
		a.lastSeen = r.StepID
		switch state.Normalize(r.StepState) {
		case state.Running, state.Blocked:
			a.current = r.StepID
		}
	}

	out := make(map[string]taskProgress, len(acc))
	for taskID, byInst := range acc {
		p := taskProgress{Instances: len(byInst)}
		// With more than one live instance there is no single "current step",
		// and picking one would be the same category error the agent column
		// made. The caller renders a fan-out marker instead.
		if len(byInst) == 1 {
			a := byInst[order[taskID][0]]
			step := a.current
			if step == "" {
				step = a.lastSeen
			}
			p.StepID = step
			p.Position, p.Total = stepPosition(workflows, a.workflowID, step)
		}
		out[taskID] = p
	}
	return out
}

// stepPosition locates a step in its workflow's declared sequence, returning a
// 1-based position and the sequence length.
//
// The sequence comes from the live configuration rather than the instance's
// stored definition snapshot. That is a deliberate trade: reading the snapshot
// would cost a query and a JSON parse per visible instance to produce a
// denominator that differs only when someone edited the workflow while a run
// was in flight. The Tasks list is a live operational view, and this keeps it to
// one query. Both values are zero when the workflow or step is not found, and
// the caller then shows the step name alone rather than a wrong fraction.
func stepPosition(workflows []config.WorkflowConfig, workflowID, stepID string) (int, int) {
	if workflowID == "" || stepID == "" {
		return 0, 0
	}
	for i := range workflows {
		if workflows[i].ID != workflowID {
			continue
		}
		steps := workflows[i].Steps
		for j := range steps {
			if steps[j].ID == stepID {
				return j + 1, len(steps)
			}
		}
		// The step exists in the run but not in the current definition — the
		// workflow was edited mid-flight. Report the length so the reader still
		// sees the scale, but not a position that would be a lie.
		return 0, len(steps)
	}
	return 0, 0
}

// progressCell renders the progress column.
//
// It replaces the agent column, which reported one arbitrary agent of however
// many a task's steps used — an attribute of a step hoisted onto a task. This
// column answers "where has it got to", which is the question the list could not
// previously answer at all.
func progressCell(p taskProgress, width int) string {
	return pad(truncate(progressLabel(p, width), width), width)
}

// progressLabel is progressCell without padding, so tests and the board can use
// the same text.
//
// The step id is truncated but the n/m suffix never is: losing the position is
// worse than losing the tail of a name, since the position is the part that
// cannot be guessed.
func progressLabel(p taskProgress, width int) string {
	switch {
	case p.Instances > 1:
		return "⑂ " + strconv.Itoa(p.Instances) + " steps"
	case p.StepID == "":
		return "—"
	case p.Total == 0:
		return p.StepID
	case p.Position == 0:
		// Step not in the current definition: show the scale, not a position.
		return truncate(p.StepID, maxInt(1, width-4)) + " ?/" + strconv.Itoa(p.Total)
	default:
		suffix := " " + strconv.Itoa(p.Position) + "/" + strconv.Itoa(p.Total)
		return truncate(p.StepID, maxInt(1, width-len(suffix))) + suffix
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// taskProgressOf reads the progress fields a TaskItem carries back into the
// value type the renderers use.
func taskProgressOf(it TaskItem) taskProgress {
	return taskProgress{
		StepID:    it.StepID,
		Position:  it.StepPosition,
		Total:     it.StepTotal,
		Instances: it.LiveInstances,
	}
}
