package daemon

import (
	"context"
	"strings"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// trustedAssociations are the GitHub author_association values that are
// considered trusted: the repository owner, an org member, or an explicitly
// invited collaborator. Every other value (CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR,
// FIRST_TIMER, MANNEQUIN, NONE) is untrusted.
var trustedAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// isTrustedAuthor reports whether a source item's author carries a trusted
// association. Items with an empty AuthorAssociation are treated as trusted so
// that sources that don't carry this field (Plane, Jira, …) are unaffected.
func isTrustedAuthor(item model.SourceItem) bool {
	if item.AuthorAssociation == "" {
		return true
	}
	return trustedAssociations[strings.ToUpper(item.AuthorAssociation)]
}

// parkUntrustedItem labels the item as needs-triage, removes the ai-ready
// label (best-effort), and logs the decision. It does not bind the item to the
// DB so untrusted items leave no task footprint.
func (d *Dispatcher) parkUntrustedItem(ctx context.Context, cell model.SourceItem, sourceID string) {
	aplog.Info("trust-gate: source %s item %s (%q): author_association=%q is not trusted — labelling needs-triage and skipping dispatch",
		sourceID, cell.LogLabel(), cell.Title, cell.AuthorAssociation)

	adapter, ok := d.sources[sourceID]
	if !ok {
		return
	}

	// Strip the ai-ready label so the item leaves the filter set and stops
	// being re-polled as a dispatch candidate. This is the same label removed
	// by the triage-gate GitHub Action (defense-in-depth layer). Best-effort.
	if remover, ok := adapter.(source.LabelRemover); ok {
		if err := remover.RemoveLabels(ctx, cell, []string{"ai-ready"}); err != nil {
			aplog.Warn("trust-gate: source %s item %s: remove ai-ready: %v", sourceID, cell.LogLabel(), err)
		}
	}

	// Mark the item as needing human triage.
	if adder, ok := adapter.(source.LabelAdder); ok {
		if err := adder.AddLabels(ctx, cell, []string{"needs-triage"}); err != nil {
			aplog.Warn("trust-gate: source %s item %s: add needs-triage: %v", sourceID, cell.LogLabel(), err)
		}
	}
}
