package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	aplog "github.com/orlandoburli/apiary/internal/log"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/source"
)

// ErrUnknownWorkflow is returned when the requested workflow id is not in the
// live config. It must fail closed: resolveWorkflow answers an unknown id with a
// zero WorkflowConfig, which would create an instance with no steps that reports
// success having done nothing.
var ErrUnknownWorkflow = errors.New("unknown workflow")

// ManualRunRequest asks the daemon to start one named workflow on demand.
type ManualRunRequest struct {
	// WorkflowID names the workflow to run. Required.
	WorkflowID string `json:"workflow_id"`
	// ItemRef optionally targets an existing source item — a cell id or the
	// item's human reference (CDT-123, #1953), the same vocabulary `apiary
	// restart` accepts. Empty runs the workflow standalone on a fresh internal
	// task with no source binding.
	ItemRef string `json:"item_ref,omitempty"`
	// Input seeds a standalone run's task input, readable from steps as
	// ${{ input.* }}. Ignored when ItemRef is set — that task's input belongs to
	// the source binding.
	Input map[string]any `json:"input,omitempty"`
	// Title overrides the generated title of a standalone run's task.
	Title string `json:"title,omitempty"`
}

// ManualRunResult reports what a manual run actually targeted. Dispatch itself is
// asynchronous — the run outlives the request that started it — so this describes
// the target and the guards bypassed, not the outcome.
type ManualRunResult struct {
	WorkflowID string `json:"workflow_id"`
	TaskID     string `json:"task_id"`
	CellID     string `json:"cell_id,omitempty"`
	Ref        string `json:"ref,omitempty"`
	// Standalone reports that no source item is bound: side effects that write
	// back to a source (comments, state locks, sub-issues) are no-ops for this run.
	Standalone bool `json:"standalone"`
	// Concurrent reports that the workflow already had a live instance on this
	// task when the run started, so this is a second one. Manual runs allow that
	// deliberately; callers surface it as a warning.
	Concurrent bool `json:"concurrent"`
	// Bypassed names the pre-dispatch guards this run did not evaluate, so a
	// bypass is never silent (mirrors RestartResult.Overridden).
	Bypassed []string `json:"bypassed,omitempty"`
}

// manualRunHandler serves POST /workflows/{id}/run.
//
// runCtx must be the daemon's lifetime context: dispatch is asynchronous, so a
// run started from the request context would be cancelled the moment the caller
// disconnects. Same reasoning as the /resume/ handler.
func (d *Dispatcher) manualRunHandler(runCtx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/workflows/")
		wfID, action, _ := strings.Cut(rest, "/")
		// Unescape: a workflow id is config-authored and may contain characters
		// the caller had to escape on the way in.
		if unescaped, err := url.PathUnescape(wfID); err == nil {
			wfID = unescaped
		}
		if action != "run" {
			http.Error(w, "not found: expected /workflows/{id}/run", http.StatusNotFound)
			return
		}

		req := ManualRunRequest{WorkflowID: wfID, ItemRef: r.URL.Query().Get("item")}
		// A body is optional; only a malformed one is an error.
		if body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)); len(bytes.TrimSpace(body)) > 0 {
			var payload struct {
				Input map[string]any `json:"input"`
				Title string         `json:"title"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			req.Input, req.Title = payload.Input, payload.Title
		}

		res, err := d.RunWorkflowManual(runCtx, req)
		if err != nil {
			http.Error(w, err.Error(), manualRunHTTPStatus(err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(res)
	}
}

// manualRunHTTPStatus maps a manual-run error to its IPC HTTP status, mirroring
// resumeHTTPStatus.
func manualRunHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrUnknownWorkflow):
		return http.StatusNotFound
	case errors.Is(err, ErrUnknownCell), errors.Is(err, ErrAmbiguousRef):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// Label returns the reference to show a human for the run's target.
func (r ManualRunResult) Label() string {
	switch {
	case r.Standalone:
		return "standalone task " + r.TaskID
	case r.Ref != "" && r.Ref != r.CellID:
		return fmt.Sprintf("%s (%s)", r.Ref, r.CellID)
	case r.CellID != "":
		return r.CellID
	default:
		return r.TaskID
	}
}

// manualBypassed is what a manual run skips, in the order the poll loop would
// have applied them. It is a constant rather than a computed list: the manual
// path never evaluates these guards at all, so there is nothing to observe.
var manualBypassed = []string{
	"trigger match (state/labels/filters)",
	"exclusive trigger suppression",
	"active instance / in-flight",
	"once",
	"consecutive-failure cap",
}

// RunWorkflowManual starts one named workflow immediately, regardless of whether
// anything would have triggered it.
//
// Every guard the poll loop applies lives on the poll path — trigger matching in
// Router.RouteAll, then dropAutoResumingMatches / dropActiveMatches /
// dropOnceMatches / dropCappedMatches. A manual run does not switch them off; it
// never enters that path. It resolves the workflow by name, builds the Match by
// hand (router.ManualMatch) and fans out directly, so a workflow runs even when
// its trigger does not match the item's current state, when its `once` is spent,
// when it is capped by consecutive failures, when an exclusive trigger would have
// claimed the task — and even when the same workflow is already running on the
// same task, which yields a second concurrent instance.
//
// Everything downstream of the guards is shared with an automatic dispatch: the
// queue (when enabled), agent semaphores, activeRuns, outstanding-workflow
// accounting, transcripts and execution events.
//
// Dispatch is asynchronous. ctx must be the daemon's lifetime context, not a
// request context, or the run is cancelled when the caller disconnects.
func (d *Dispatcher) RunWorkflowManual(ctx context.Context, req ManualRunRequest) (ManualRunResult, error) {
	wfID := strings.TrimSpace(req.WorkflowID)
	res := ManualRunResult{WorkflowID: wfID, Bypassed: manualBypassed}

	wf, ok := d.workflowByID(wfID)
	if !ok {
		return res, fmt.Errorf("%w: %q (known: %s)", ErrUnknownWorkflow, wfID, strings.Join(d.workflowIDs(), ", "))
	}
	match, ok := router.ManualMatch(wf)
	if !ok {
		// Only reachable for a workflow with an empty id, which config validation
		// rejects; guard anyway rather than dispatch something unresolvable.
		return res, fmt.Errorf("%w: %q cannot be resolved to a route", ErrUnknownWorkflow, wfID)
	}

	var (
		cell      model.SourceItem
		adapter   source.Adapter
		task      model.InternalTask
		persisted bool
		err       error
	)
	if strings.TrimSpace(req.ItemRef) == "" {
		task, err = d.standaloneTask(ctx, wf, req)
		if err != nil {
			return res, err
		}
		persisted = true
		res.Standalone = true
		// The engine's sourceItemView falls back to the task when there are no
		// bindings; mirroring that here keeps logs and activeRuns readable.
		cell = model.SourceItem{ID: task.ID, Title: task.Title}
	} else {
		cell, adapter, task, persisted, err = d.manualTargetItem(ctx, req.ItemRef)
		if err != nil {
			return res, err
		}
		res.CellID = cell.ID
	}
	res.TaskID = task.ID
	if res.Ref == "" && res.CellID != "" {
		if _, number, refErr := d.resolveCellRef(ctx, req.ItemRef); refErr == nil {
			res.Ref = number
		}
	}

	// Report — never block on — an existing live instance. Starting a second one
	// is the documented behaviour, but an operator who did not mean to should see
	// it in the response, not discover it later in the instance list.
	if persisted && d.db != nil {
		if active, err := d.db.HasActiveInstanceForRoute(ctx, task.ID, wf.ID); err != nil {
			aplog.Debug("manual run %s: checking live instances: %v", wf.ID, err)
		} else {
			res.Concurrent = active
		}
	}

	aplog.Info("manual run: dispatching workflow %s on %s [agent %s] — bypassing %s%s",
		wf.ID, res.Label(), match.Route.Agent, strings.Join(manualBypassed, ", "),
		map[bool]string{true: " (a live instance already exists — starting a second)", false: ""}[res.Concurrent])

	// A per-run nonce, so a repeat manual run is a second job rather than a
	// duplicate swallowed by the queue's idempotency key. Bumping the dispatch
	// generation would free the key too, but that is restart's semantics — it
	// invalidates the keys of rounds that are legitimately in flight.
	d.fanOut(ctx, cell, adapter, task, persisted, []router.Match{match}, fanOutOpts{
		idemSuffix: "manual-" + manualRunNonce(),
		// The manual path never stores d.inFlight[cell.ID]: it does not use it as
		// a guard, and clearing an entry a concurrent poll owns would let the next
		// tick dispatch that cell a second time.
		ownsInFlight: false,
	})
	return res, nil
}

// workflowIDs returns every configured workflow id, sorted — the "did you mean"
// list on an unknown id.
func (d *Dispatcher) workflowIDs() []string {
	if d.cfg == nil {
		return nil
	}
	ids := make([]string, 0, len(d.cfg.Workflows))
	for _, wf := range d.cfg.Workflows {
		if wf.ID != "" {
			ids = append(ids, wf.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// standaloneTask creates the internal task a source-less manual run executes
// against. It follows the spawned-task convention (model.InternalTask): no
// binding, no source in metadata, structured Input readable as ${{ input.* }}.
func (d *Dispatcher) standaloneTask(ctx context.Context, wf config.WorkflowConfig, req ManualRunRequest) (model.InternalTask, error) {
	if d.db == nil {
		return model.InternalTask{}, errors.New("manual run without an item needs a database — none is configured")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "manual run: " + wf.ID
	}
	task := model.InternalTask{
		Title:       title,
		Description: wf.Description,
		Input:       req.Input,
		State:       model.TaskStateRegistered,
		Metadata:    model.TaskMetadata{Type: "manual"},
	}
	if err := d.db.InternalTasks().CreateTask(ctx, &task); err != nil {
		return model.InternalTask{}, fmt.Errorf("create task for manual run of %s: %w", wf.ID, err)
	}
	aplog.Info("manual run: created standalone task %s (%q) for workflow %s", task.ID, task.Title, wf.ID)
	return task, nil
}

// manualTargetItem resolves an item reference to the cell, adapter and bound task
// a manual run executes against — the same binding a poll of that item produces,
// so the run sees live labels and state.
//
// It fails closed. A reference that resolves to no known item returns
// ErrUnknownCell and creates nothing: silently falling back to a standalone task
// would run the workflow against an empty target while reporting success, which
// is the mis-targeting class of bug #377 was about.
func (d *Dispatcher) manualTargetItem(ctx context.Context, ref string) (model.SourceItem, source.Adapter, model.InternalTask, bool, error) {
	cellID, _, err := d.resolveCellRef(ctx, ref)
	if err != nil {
		return model.SourceItem{}, nil, model.InternalTask{}, false, err
	}
	if err := d.assertKnownCell(ctx, cellID); err != nil {
		return model.SourceItem{}, nil, model.InternalTask{}, false, err
	}

	cell, adapter, ok := d.fetchCell(ctx, cellID)
	if !ok {
		// No source can read the item right now (no TaskPoller, or the fetch
		// failed). The binding still knows what the task is, so the workflow can
		// run — it just routes on the last-known attributes and cannot write back
		// through an adapter.
		aplog.Warn("manual run: no source could fetch item %s — running on its last known state", cellID)
		cell = model.SourceItem{ID: cellID}
		if b, err := d.db.SourceBindings().GetBindingBySourceItemID(ctx, cellID); err == nil && b != nil {
			cell.SourceID = b.SourceID
			if t, err := d.db.InternalTasks().GetTask(ctx, b.TaskID); err == nil && t != nil {
				return cell, nil, *t, true, nil
			}
		}
	}

	task, persisted := d.bindItem(ctx, cell)
	return cell, adapter, task, persisted, nil
}

// fetchCell reads a source item from whichever configured source can poll it,
// returning the adapter alongside so side effects can write back. It is the
// read-only half of ForceRestart's label-stripping loop: same TaskPoller scan and
// same refusal to accept a substituted item, without touching anything.
func (d *Dispatcher) fetchCell(ctx context.Context, cellID string) (model.SourceItem, source.Adapter, bool) {
	for _, sc := range d.cfg.Sources {
		adapter, ok := d.sources[sc.ID]
		if !ok {
			continue
		}
		poller, ok := adapter.(source.TaskPoller)
		if !ok {
			continue
		}
		cell, err := poller.PollTask(ctx, cellID)
		if err != nil {
			aplog.Debug("manual run: %s cannot fetch item %s: %v", sc.ID, cellID, err)
			continue
		}
		if cell.ID != "" && cell.ID != cellID {
			aplog.Error("manual run: %s returned item %s for %s — ignoring it", sc.ID, cell.ID, cellID)
			continue
		}
		cell.ID = cellID
		return cell, adapter, true
	}
	return model.SourceItem{}, nil, false
}

// manualRunNonce returns 64 bits of randomness as hex — enough to keep two manual
// runs of the same workflow on the same task from sharing a queue idempotency key.
func manualRunNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; if it ever does, a colliding key
		// only means the second run de-duplicates, which is safe.
		return "0"
	}
	return hex.EncodeToString(b[:])
}
