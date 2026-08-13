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

// ErrUnknownSource is returned when an explicitly named source is not configured.
var ErrUnknownSource = errors.New("unknown source")

// ErrSourceRequired is returned when an item reference is not a known cell and
// more than one source could hold it. Guessing would mean fetching some other
// project's PSP-199 and running a workflow over it, so the caller is asked.
var ErrSourceRequired = errors.New("source required")

// ManualRunRequest asks the daemon to start one named workflow on demand.
type ManualRunRequest struct {
	// WorkflowID names the workflow to run. Required.
	WorkflowID string `json:"workflow_id"`
	// ItemRef optionally targets an existing source item — a cell id or the
	// item's human reference (CDT-123, #1953), the same vocabulary `apiary
	// restart` accepts. Empty runs the workflow standalone on a fresh internal
	// task with no source binding.
	ItemRef string `json:"item_ref,omitempty"`
	// SourceID optionally names which source ItemRef belongs to. It is needed
	// only when the reference is ambiguous, or when it names an item apiary has
	// never polled and more than one source could fetch it.
	SourceID string `json:"source_id,omitempty"`
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
	// SourceID is the source the item was resolved against — worth echoing back
	// when it was inferred rather than named.
	SourceID string `json:"source_id,omitempty"`
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

		req := ManualRunRequest{
			WorkflowID: wfID,
			ItemRef:    r.URL.Query().Get("item"),
			SourceID:   r.URL.Query().Get("source"),
		}
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
	case errors.Is(err, ErrUnknownCell), errors.Is(err, ErrUnknownSource),
		errors.Is(err, ErrAmbiguousRef), errors.Is(err, ErrSourceRequired):
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
		cell, adapter, task, persisted, err = d.manualTargetItem(ctx, req.ItemRef, req.SourceID)
		if err != nil {
			return res, err
		}
		res.CellID, res.SourceID, res.Ref = cell.ID, cell.SourceID, cell.Number
	}
	res.TaskID = task.ID
	if res.Ref == "" && res.CellID != "" {
		// The item carried no human reference of its own; fall back to whatever
		// the binding recorded.
		if _, number, refErr := d.resolveCellRef(ctx, req.ItemRef, req.SourceID); refErr == nil {
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
// An item apiary has never polled is fetched from its source and bound on the
// spot (discoverItem), which is how `--item PSP-199` works for a ticket outside
// the source's poll filters. It still fails closed: a reference no source can
// produce an item for returns ErrUnknownCell and creates nothing, rather than
// running the workflow against an empty target and reporting success (the
// mis-targeting class of bug #377 was about).
//
// sourceID scopes both halves: it disambiguates a reference that exists in more
// than one source, and it names which source to fetch an unknown one from.
func (d *Dispatcher) manualTargetItem(ctx context.Context, ref, sourceID string) (model.SourceItem, source.Adapter, model.InternalTask, bool, error) {
	fail := func(err error) (model.SourceItem, source.Adapter, model.InternalTask, bool, error) {
		return model.SourceItem{}, nil, model.InternalTask{}, false, err
	}
	if sourceID != "" {
		if _, err := d.manualSource(sourceID); err != nil {
			return fail(err)
		}
	}

	cellID, _, err := d.resolveCellRef(ctx, ref, sourceID)
	if err != nil {
		return fail(err)
	}
	if err := d.assertKnownCell(ctx, cellID); err != nil {
		if !errors.Is(err, ErrUnknownCell) {
			return fail(err)
		}
		// Not a cell apiary has seen. Ask a source for it before giving up.
		cell, adapter, derr := d.discoverItem(ctx, ref, sourceID)
		if derr != nil {
			return fail(derr)
		}
		task, persisted := d.bindItem(ctx, cell)
		return cell, adapter, task, persisted, nil
	}

	cell, adapter, ok := d.fetchCell(ctx, cellID, sourceID)
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

// discoverItem fetches an item apiary has never polled, straight from its source.
//
// This is the difference between "run a workflow on a task apiary is tracking"
// and "run a workflow on PSP-199": a ticket outside the source's poll filters, in
// a state the filters exclude, or created since the last tick has no binding, so
// reference resolution finds nothing. The adapter can still resolve it — Jira's
// /issue/{idOrKey} takes the key, GitHub's /issues/{n} the number — so ask.
//
// Unlike fetchCell this accepts the id the adapter reports rather than requiring
// it to equal what was asked for: the caller passed a human reference (PSP-199)
// precisely because it does not know the cell id (the opaque numeric issue id),
// and resolving one to the other is what the fetch is for. The returned item is
// echoed back to the caller and logged, so a fetch that resolved to something
// unexpected is visible rather than silent.
func (d *Dispatcher) discoverItem(ctx context.Context, ref, sourceID string) (model.SourceItem, source.Adapter, error) {
	id, err := d.manualSourceForDiscovery(sourceID)
	if err != nil {
		return model.SourceItem{}, nil, err
	}
	adapter := d.sources[id]

	cell, err := adapter.(source.TaskPoller).PollTask(ctx, strings.TrimSpace(ref))
	if err != nil {
		return model.SourceItem{}, nil, fmt.Errorf("%w: %s could not fetch %q: %w", ErrUnknownCell, id, ref, err)
	}
	if cell.ID == "" {
		// A source that answers with no id has nothing to bind or write back to.
		return model.SourceItem{}, nil, fmt.Errorf("%w: %s returned no item for %q", ErrUnknownCell, id, ref)
	}
	// The source we asked is authoritative, not the one the item claims: the
	// binding and the adapter that later writes comments and state back must name
	// the same source, or the run reads from one and writes to another.
	if cell.SourceID != "" && cell.SourceID != id {
		aplog.Warn("manual run: %s returned an item tagged source %q for %q — binding it to %s, the source that answered",
			id, cell.SourceID, ref, id)
	}
	cell.SourceID = id
	aplog.Info("manual run: %q is not a known cell — fetched it from source %s as %s (%q)",
		ref, id, cell.LogLabel(), cell.Title)
	return cell, adapter, nil
}

// manualSource returns the configured, connected source with this id.
func (d *Dispatcher) manualSource(id string) (source.Adapter, error) {
	if adapter, ok := d.sources[id]; ok {
		return adapter, nil
	}
	return nil, fmt.Errorf("%w: %q (configured: %s)", ErrUnknownSource, id, strings.Join(d.sourceIDs(), ", "))
}

// manualSourceForDiscovery picks the source to fetch an unknown reference from.
//
// Named explicitly, it must exist and be able to fetch a single item. Left out,
// it is inferred only when the answer is unambiguous — exactly one source can do
// it. With several, the caller is asked rather than guessed at: fetching the
// wrong project's PSP-199 and running a workflow over it is not an error anyone
// would catch quickly.
func (d *Dispatcher) manualSourceForDiscovery(sourceID string) (string, error) {
	if sourceID != "" {
		adapter, err := d.manualSource(sourceID)
		if err != nil {
			return "", err
		}
		if _, ok := adapter.(source.TaskPoller); !ok {
			return "", fmt.Errorf("%w: source %q cannot fetch a single item (no TaskPoller)", ErrUnknownCell, sourceID)
		}
		return sourceID, nil
	}

	var candidates []string
	for _, sc := range d.cfg.Sources {
		if adapter, ok := d.sources[sc.ID]; ok {
			if _, ok := adapter.(source.TaskPoller); ok {
				candidates = append(candidates, sc.ID)
			}
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf("%w: no configured source can fetch a single item", ErrUnknownCell)
	default:
		sort.Strings(candidates)
		return "", fmt.Errorf("%w: %d sources could hold it (%s) — pass --source to say which",
			ErrSourceRequired, len(candidates), strings.Join(candidates, ", "))
	}
}

// sourceIDs returns every configured source id, sorted.
func (d *Dispatcher) sourceIDs() []string {
	if d.cfg == nil {
		return nil
	}
	ids := make([]string, 0, len(d.cfg.Sources))
	for _, sc := range d.cfg.Sources {
		ids = append(ids, sc.ID)
	}
	sort.Strings(ids)
	return ids
}

// fetchCell reads a known cell from whichever configured source can poll it,
// returning the adapter alongside so side effects can write back. It is the
// read-only half of ForceRestart's label-stripping loop: same TaskPoller scan and
// same refusal to accept a substituted item, without touching anything.
//
// sourceID, when set, restricts the scan to that source.
func (d *Dispatcher) fetchCell(ctx context.Context, cellID, sourceID string) (model.SourceItem, source.Adapter, bool) {
	for _, sc := range d.cfg.Sources {
		if sourceID != "" && sc.ID != sourceID {
			continue
		}
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
