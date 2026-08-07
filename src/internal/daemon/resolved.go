package daemon

import (
	"context"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/source"
)

// liveInstanceStates are the states a workflow instance can be in while its
// source item still matters: actively running, or parked waiting for something
// that will never come once the item is gone.
var liveInstanceStates = []string{
	db.InstanceStateRunning,
	db.InstanceStateWaiting,
	db.InstanceStateApprovalWaiting,
}

// checkResolved stops workflow instances whose source item is no longer active
// — an alert that stopped firing while the investigation was still running.
//
// Opt-in per source (interrupt_on_resolve) and only for adapters that can tell
// a resolved item from a merely invisible one (source.ItemResolver). The
// default across every source stays "let the run finish": an investigation's
// findings usually outlive the alert that prompted them, and interrupting on
// resolution throws away work that was nearly done.
//
// Fails closed at every step. If the source cannot answer, nothing is stopped.
func (d *Dispatcher) checkResolved(ctx context.Context, sc config.SourceConfig, adapter source.Adapter) {
	if !sc.InterruptOnResolve || d.db == nil {
		return
	}
	resolver, ok := adapter.(source.ItemResolver)
	if !ok {
		// Validation rejects this combination; a daemon running an older config
		// should still say why the policy is doing nothing rather than be silent.
		aplog.Warn("source %s: interrupt_on_resolve is set but the adapter cannot report resolved items — ignoring", sc.ID)
		return
	}

	// Map item id → instances, since one item can drive several workflows.
	instancesByItem := map[string][]db.WorkflowInstance{}
	for _, state := range liveInstanceStates {
		instances, err := d.db.ListWorkflowInstancesByState(ctx, state)
		if err != nil {
			aplog.Error("source %s: interrupt_on_resolve: listing %s instances: %v", sc.ID, state, err)
			return
		}
		for _, inst := range instances {
			if inst.SourceID != sc.ID || inst.CellID == "" {
				continue
			}
			instancesByItem[inst.CellID] = append(instancesByItem[inst.CellID], inst)
		}
	}
	if len(instancesByItem) == 0 {
		return
	}

	itemIDs := make([]string, 0, len(instancesByItem))
	for id := range instancesByItem {
		itemIDs = append(itemIDs, id)
	}

	resolved, err := resolver.ResolvedItems(ctx, itemIDs)
	if err != nil {
		// "Could not tell" is not "resolved". Leave everything running.
		aplog.Error("source %s: interrupt_on_resolve: %v — leaving %d in-flight instance(s) running", sc.ID, err, len(instancesByItem))
		return
	}

	for _, itemID := range resolved {
		for _, inst := range instancesByItem[itemID] {
			aplog.Info("source %s: item %s resolved — stopping workflow %q instance %s (was %s)",
				sc.ID, itemID, inst.WorkflowID, inst.ID, inst.State)
			if err := d.StopInstance(ctx, inst.ID); err != nil {
				aplog.Error("source %s: stopping instance %s after resolve: %v", sc.ID, inst.ID, err)
			}
		}
	}
}
