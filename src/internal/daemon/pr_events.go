package daemon

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
	"github.com/orlandoburli/apiary/internal/source"
)

// prEventOverlap is re-fetched behind the watermark on every event poll, so a
// comment that landed while the previous poll ran is never missed. The
// persistent dispatch claim (pr_event_dispatches) makes the overlap safe: a
// re-fetched event can never dispatch twice.
const prEventOverlap = 2 * time.Minute

// pollPREvents fetches and routes pull-request events for one source, called
// from the poll cycle after item polling. Capability-gated: sources that do not
// implement PREventPoller — or configs with no event trigger — skip it entirely.
//
// The watermark is persisted per source. A source with no watermark yet is
// baselined to "now" without dispatching, so enabling event triggers on a repo
// with years of comments does not storm over history. The watermark only
// advances after a successful poll, so a transient API failure retries the same
// window on the next cycle.
func (d *Dispatcher) pollPREvents(ctx context.Context, sc config.SourceConfig, adapter source.Adapter) {
	poller, ok := adapter.(source.PREventPoller)
	if !ok || d.db == nil || !d.router.HasEventRoutes() {
		return
	}

	wm, err := d.db.GetPREventWatermark(ctx, sc.ID)
	if err != nil {
		aplog.Error("source %s: read PR event watermark: %v", sc.ID, err)
		return
	}
	pollStart := time.Now()
	if wm.IsZero() {
		if err := d.db.SetPREventWatermark(ctx, sc.ID, pollStart); err != nil {
			aplog.Error("source %s: baseline PR event watermark: %v", sc.ID, err)
			return
		}
		aplog.Info("source %s: PR event polling baselined — events before now are ignored", sc.ID)
		return
	}

	events, err := poller.PollPREvents(ctx, wm.Add(-prEventOverlap))
	if err != nil {
		aplog.Error("source %s: poll PR events: %v", sc.ID, err)
		return
	}
	if len(events) > 0 {
		aplog.Info("source %s: found %d PR event(s)", sc.ID, len(events))
	}
	for _, ev := range events {
		d.routePREvent(ctx, ev)
	}
	if err := d.db.SetPREventWatermark(ctx, sc.ID, pollStart); err != nil {
		aplog.Error("source %s: advance PR event watermark: %v", sc.ID, err)
	}
}

// routePREvent routes one PR event through the event triggers and dispatches
// every match exactly once (guarded by the persistent per-(event, workflow)
// claim). The workflow instance binds to the InternalTask of the ticket the PR
// is about — resolved from the PR title, head branch, then body — so the task
// list shows the ticket, not a "PR #nnn" event receipt. An event whose ticket
// resolves to no bound task is dropped rather than materialising a synthetic
// per-PR task.
func (d *Dispatcher) routePREvent(ctx context.Context, ev model.SourceEvent) {
	related, dropReason := d.resolveEventTask(ctx, ev)
	if related == nil {
		aplog.Info("event %s (%s on PR #%d by %s): dropped — %s", ev.ID, ev.Kind, ev.PRNumber, ev.Author, dropReason)
		return
	}

	matches := d.router.RouteEvent(ev, related)
	if len(matches) == 0 {
		aplog.Debug("event %s (%s on PR #%d by %s): no matching trigger", ev.ID, ev.Kind, ev.PRNumber, ev.Author)
		return
	}

	task, persisted := *related, true

	for _, m := range matches {
		// Runaway-loop budget: cap dispatches per (workflow, PR). Fail closed on a
		// count error — skip this poll, the claim below still prevents duplicates.
		if budget := m.Route.MaxDispatches; budget > 0 {
			n, err := d.db.CountPREventDispatches(ctx, ev.SourceID, m.Route.ID, ev.PRNumber)
			if err != nil {
				aplog.Error("event %s: count dispatches for workflow %s: %v — skipping this poll", ev.ID, m.Route.ID, err)
				continue
			}
			if n >= budget {
				aplog.Warn("event %s: workflow %s hit max_dispatches (%d/%d) for PR #%d, not dispatching",
					ev.ID, m.Route.ID, n, budget, ev.PRNumber)
				continue
			}
		}

		// Exactly-once claim: the insert either wins (dispatch now) or the event
		// already dispatched this workflow — on this or any previous daemon run.
		claimed, err := d.db.ClaimPREventDispatch(ctx, ev.SourceID, ev.ID, m.Route.ID, ev.PRNumber)
		if err != nil {
			aplog.Error("event %s: claim dispatch for workflow %s: %v — skipping this poll", ev.ID, m.Route.ID, err)
			continue
		}
		if !claimed {
			aplog.Debug("event %s: workflow %s already dispatched, skipping", ev.ID, m.Route.ID)
			continue
		}
		d.dispatchPREvent(ctx, ev, task, persisted, m)
	}
}

// resolveEventTask resolves the ticket a PR event is about to its bound
// InternalTask. It returns nil plus a human-readable reason when the event must
// be dropped.
//
// Two kinds of reference are understood:
//
//   - RelatedItemID — the source-native id of the GitHub issue referenced by the
//     PR body ("Closes #42"). This is the explicit origin and wins when the
//     issue is bound, because a closing keyword is a stronger signal than a
//     passing mention elsewhere.
//   - TicketRefs — human ticket keys (PSP-348) found in the PR title, head
//     branch, then body. They are tried in that order against the bindings'
//     SourceItemNumber, because a ticket key is what people and bindings
//     actually share (Jira binds on the opaque numeric issue id).
//
// A reference that matches distinct bound items is ambiguous: the event is
// dropped and logged rather than guessed at, mirroring the #377 guidance for
// restart references. An unbound candidate is skipped so a false-positive key
// in a title ("Fix UTF-8 decoding for PSP-348") never hides the real one.
func (d *Dispatcher) resolveEventTask(ctx context.Context, ev model.SourceEvent) (*model.InternalTask, string) {
	if ev.RelatedItemID != "" {
		if b, _ := d.db.SourceBindings().GetBindingBySourceItem(ctx, ev.SourceID, ev.RelatedItemID); b != nil {
			if t, _ := d.db.InternalTasks().GetTask(ctx, b.TaskID); t != nil {
				aplog.Debug("event %s: related item %s/%s → task %s", ev.ID, ev.SourceID, ev.RelatedItemID, t.ID)
				return t, ""
			}
		}
	}

	if len(ev.TicketRefs) == 0 {
		return nil, "no ticket reference in the PR title, head branch, or body"
	}

	var unbound []string
	for _, ref := range ev.TicketRefs {
		bindings, err := d.db.SourceBindings().ListBindingsBySourceItemNumber(ctx, ref)
		if err != nil {
			aplog.Error("event %s: look up ticket reference %q: %v", ev.ID, ref, err)
			continue
		}

		// Several bindings are fine when they are the same item (a source item can
		// be bound more than once over its life); distinct items are not. The list
		// is oldest-first, so a map assignment keeps the newest binding per item.
		distinct := map[string]model.SourceBinding{}
		for _, b := range bindings {
			distinct[b.SourceID+":"+b.SourceItemID] = b
		}
		if len(distinct) == 0 {
			unbound = append(unbound, ref)
			continue
		}
		if len(distinct) > 1 {
			keys := make([]string, 0, len(distinct))
			for k := range distinct {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			aplog.Warn("event %s: ticket reference %q matches %d bound items (%s); dropping the event rather than guessing which one it means",
				ev.ID, ref, len(distinct), strings.Join(keys, ", "))
			return nil, fmt.Sprintf("ticket reference %q is ambiguous (%s)", ref, strings.Join(keys, ", "))
		}
		for _, b := range distinct {
			t, err := d.db.InternalTasks().GetTask(ctx, b.TaskID)
			if err != nil {
				aplog.Error("event %s: read task %s for ticket reference %q: %v", ev.ID, b.TaskID, ref, err)
				unbound = append(unbound, ref)
				continue
			}
			if t == nil {
				aplog.Warn("event %s: ticket reference %q is bound to missing task %s", ev.ID, ref, b.TaskID)
				unbound = append(unbound, ref)
				continue
			}
			aplog.Debug("event %s: ticket reference %q → task %s (source %s)", ev.ID, ref, t.ID, b.SourceID)
			return t, ""
		}
	}

	return nil, fmt.Sprintf("ticket reference(s) %s not bound to a task", strings.Join(unbound, ", "))
}

// dispatchPREvent launches one workflow for one claimed PR event, mirroring
// fanOut's admission (per-agent semaphore, active-run tracking, outstanding
// counter) for a single match. The event payload rides in on the workflow env
// overlay (APIARY_EVENT_* / APIARY_PR_*) and the engine's event scope
// (${{ event.* }}).
func (d *Dispatcher) dispatchPREvent(ctx context.Context, ev model.SourceEvent, task model.InternalTask, persisted bool, match router.Match) {
	if persisted && d.db != nil {
		if _, err := d.db.InternalTasks().IncrementOutstanding(ctx, task.ID, 1); err != nil {
			aplog.Error("task %s: increment outstanding: %v", task.ID, err)
		}
	}

	agentCh := d.agentSem[match.Route.Agent]
	runID := d.nextRunID()
	title := fmt.Sprintf("%s on PR #%d", ev.Kind, ev.PRNumber)
	cell := model.SourceItem{
		ID:       ev.RelatedItemID,
		SourceID: ev.SourceID,
		Number:   fmt.Sprintf("#%d", ev.PRNumber),
		Title:    title,
		URL:      ev.PRURL,
	}
	if cell.ID == "" && d.db != nil {
		// The task is bound to the ticket, not to a synthetic PR cell; carry the
		// ticket's primary binding into the active-run view so cancel-by-cell and
		// the dashboard's drill key resolve it.
		if bindings, err := d.db.ListBindingsByTask(ctx, task.ID); err == nil && len(bindings) > 0 {
			b := bindings[0]
			cell.ID, cell.SourceID = b.SourceItemID, b.SourceID
			cell.Number, cell.URL = b.SourceItemNumber, b.SourceItemURL
		}
	}

	d.goBackground(func() {
		if agentCh != nil {
			select {
			case agentCh <- struct{}{}:
			case <-ctx.Done():
				return // shutting down before a slot freed; never acquired
			}
			defer func() { <-agentCh }()
		}

		d.active.Add(1)
		defer d.active.Add(-1)
		d.activeRuns.Store(runID, model.ActiveRun{
			ID:        runID,
			Cell:      cell,
			WorkerID:  match.Route.Agent,
			Status:    model.RunStatusRunning,
			StartedAt: time.Now(),
		})
		defer d.activeRuns.Delete(runID)

		aplog.Info("dispatching PR event %s (%s on PR #%d by %s) → workflow %s [agent %s]",
			ev.ID, ev.Kind, ev.PRNumber, ev.Author, match.Route.ID, match.Route.Agent)
		d.recordExecutionEvent(ctx, db.ExecutionEvent{Type: "pr_event.dispatched", TaskID: task.ID, WorkflowID: match.Route.ID,
			Metadata: map[string]any{"event_id": ev.ID, "kind": ev.Kind, "pr_number": ev.PRNumber, "author": ev.Author}})

		wf := d.resolveWorkflow(match)
		wf.Env = overlayEnv(wf.Env, prEventEnv(ev))

		instID, success, err := d.workflowEngine().RunInstanceForEvent(ctx, wf, task, prEventScope(ev))
		if err != nil {
			aplog.Error("event %s: workflow run failed: %v", ev.ID, err)
			return
		}
		aplog.Info("event %s: workflow instance %s started (success=%v)", ev.ID, instID, success)
	})
}

// prEventEnv is the environment payload exported to every step of an
// event-triggered workflow instance (overlaid on workflow-scope env, still
// overridable per step).
func prEventEnv(ev model.SourceEvent) map[string]string {
	return map[string]string{
		"APIARY_EVENT_KIND":      ev.Kind,
		"APIARY_EVENT_AUTHOR":    ev.Author,
		"APIARY_EVENT_PR_AUTHOR": ev.PRAuthor,
		"APIARY_EVENT_BODY":      ev.Body,
		"APIARY_PR_NUMBER":       strconv.Itoa(ev.PRNumber),
		"APIARY_PR_URL":          ev.PRURL,
	}
}

// prEventScope is the ${{ event.* }} expression scope for an event-triggered
// instance.
func prEventScope(ev model.SourceEvent) map[string]string {
	return map[string]string{
		"kind":               ev.Kind,
		"body":               ev.Body,
		"author":             ev.Author,
		"author_association": ev.AuthorAssociation,
		"pr_number":          strconv.Itoa(ev.PRNumber),
		"pr_url":             ev.PRURL,
		"pr_author":          ev.PRAuthor,
	}
}

// overlayEnv returns base with overlay applied on top, without mutating base
// (the workflow config's Env map is shared with the live config).
func overlayEnv(base, overlay map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}
