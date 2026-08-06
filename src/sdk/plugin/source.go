package plugin

// Wire contract for CapabilitySource plugins. The host bridges a `type:
// plugin` source in apiary.yaml to one plugin instance and invokes these
// methods over the standard single-shot protocol; the plugin polls its
// backing system and returns work items. Source plugins are read-only, like
// the in-tree monitoring sources: items they surface cannot host approval
// steps, CI waits, or label/state write-back.
const (
	// SourceMethodPoll returns the current work items (SourcePollResult).
	// Invoked on the source's poll interval with a SourcePollRequest. Items
	// must carry stable IDs: the host dispatches each distinct ID at most
	// once per its dedup rules, and an item returned on every poll while it
	// stays relevant is the expected shape (mirror of the in-tree
	// monitoring adapters).
	SourceMethodPoll = "poll"

	// SourceMethodAcknowledge is invoked after an item has been dispatched
	// (SourceAckRequest). Plugins with nothing to mark should return
	// SourceOKResult{OK: true} rather than an error.
	SourceMethodAcknowledge = "acknowledge"

	// SourceMethodWriteResult is invoked with the run outcome for an item
	// (SourceWriteResultRequest). Plugins with no result surface should
	// return SourceOKResult{OK: true} rather than an error.
	SourceMethodWriteResult = "write_result"
)

// SourceItem is one unit of work surfaced by a source plugin.
type SourceItem struct {
	// ID is the source-native identifier and the dedup key — it must be
	// stable for the lifetime of the item and unique per dispatch-worthy
	// occurrence. Items without an ID are dropped by the host.
	ID string `json:"id"`
	// Number is the human-facing reference (e.g. "INC-42"). Defaults to ID.
	Number string `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	// Description becomes the task body handed to the agent.
	Description string `json:"description,omitempty"`
	// Labels drive workflow trigger matching; use "key:value" form.
	Labels   []string `json:"labels,omitempty"`
	Type     string   `json:"type,omitempty"`
	Priority string   `json:"priority,omitempty"`
	State    string   `json:"state,omitempty"`
	URL      string   `json:"url,omitempty"`
	// Metadata is carried opaquely onto the task.
	Metadata map[string]any `json:"metadata,omitempty"`
	// CreatedAt / UpdatedAt are RFC3339 timestamps; empty means "now".
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SourcePollRequest is the payload of SourceMethodPoll.
type SourcePollRequest struct {
	// Since is the previous poll time (RFC3339), empty on the first poll.
	// Informational: plugins that re-return ongoing items may ignore it.
	Since string `json:"since,omitempty"`
	// States / Labels are the source's `filters` from apiary.yaml, forwarded
	// so the plugin can filter at the backend where possible.
	States []string `json:"states,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// SourcePollResult is the result of SourceMethodPoll.
type SourcePollResult struct {
	Items []SourceItem `json:"items"`
}

// SourceAckRequest is the payload of SourceMethodAcknowledge.
type SourceAckRequest struct {
	Item SourceItem `json:"item"`
	// Action is the host's ack action (e.g. "dispatched").
	Action string `json:"action"`
}

// SourceWriteResultRequest is the payload of SourceMethodWriteResult.
type SourceWriteResultRequest struct {
	Item    SourceItem `json:"item"`
	Success bool       `json:"success"`
	Output  string     `json:"output,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// SourceOKResult is the conventional result for acknowledge/write_result.
type SourceOKResult struct {
	OK bool `json:"ok"`
}
