// Package pluginsource bridges an out-of-process source plugin
// (CapabilitySource, protocol 1) to the source.Adapter interface, so third
// parties can ship poll-mode sources without a fork. A source declares
// `type: plugin` and names the plugin instance in config:
//
//	plugins:
//	  - id: com.example.nagios
//	sources:
//	  - id: nagios-alerts
//	    type: plugin
//	    config:
//	      plugin: com.example.nagios
//	    poll_interval: 30s
//
// The daemon injects a lookup over the enabled CapabilitySource plugin
// clients (BindPluginLookup) before Connect; each Poll/Acknowledge/
// WriteResult then becomes one single-shot plugin invocation (see
// sdk/plugin/source.go for the wire contract). Polling only: like every
// Apiary source, the plugin is asked for items on the source's interval —
// plugins never push into the daemon.
//
// Plugin sources are read-only work items, like the in-tree monitoring
// sources: none of the optional write capabilities are implemented, so
// config validation rejects workflows that need set_state, label writes,
// approvals, or CI waits against them (config.SourceCapabilities).
package pluginsource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

func init() {
	source.Register("plugin", func() source.Adapter { return &Adapter{} })
}

// Invoker is the slice of plugin.Client the bridge needs; narrowed so tests
// stub it without subprocesses.
type Invoker interface {
	ID() string
	Invoke(ctx context.Context, capability pluginsdk.Capability, method string, payload any, result any) error
}

// Adapter implements source.Adapter over a source-capable plugin client.
type Adapter struct {
	id     string
	lookup func(pluginID string) (Invoker, bool)
	client Invoker

	filterStates []string
	filterLabels []string
}

func (a *Adapter) ID() string { return a.id }

// SetID sets the source ID for this adapter.
func (a *Adapter) SetID(id string) { a.id = id }

// BindPluginLookup injects the resolver over enabled source-capable plugin
// clients. The daemon calls it before Connect; a nil or missing lookup makes
// Connect fail rather than silently polling nothing.
func (a *Adapter) BindPluginLookup(lookup func(pluginID string) (Invoker, bool)) {
	a.lookup = lookup
}

// SetFilters stores the source's filters; they are forwarded on every poll so
// the plugin can filter at its backend.
func (a *Adapter) SetFilters(states, labels []string) {
	a.filterStates = states
	a.filterLabels = labels
}

// Connect resolves the configured plugin instance. No invocation is made:
// plugins are single-shot processes with nothing persistent to connect to,
// and the first poll surfaces runtime problems on the normal error path.
func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	pluginID, _ := cfg["plugin"].(string)
	if strings.TrimSpace(pluginID) == "" {
		return fmt.Errorf("plugin source: config.plugin is required (the id of an installed, enabled plugin with the \"source\" capability)")
	}
	if a.lookup == nil {
		return fmt.Errorf("plugin source: no plugin registry available (daemon wiring)")
	}
	client, ok := a.lookup(pluginID)
	if !ok {
		return fmt.Errorf("plugin source: plugin %q is not an enabled plugin with the \"source\" capability — declare it under plugins: and check `apiary validate`", pluginID)
	}
	a.client = client
	aplog.Info("plugin source %s: bridged to plugin %s", a.id, client.ID())
	return nil
}

// Poll asks the plugin for its current items via one source.poll invocation.
func (a *Adapter) Poll(ctx context.Context, since time.Time) ([]model.SourceItem, error) {
	req := pluginsdk.SourcePollRequest{States: a.filterStates, Labels: a.filterLabels}
	if !since.IsZero() {
		req.Since = since.UTC().Format(time.RFC3339)
	}
	var res pluginsdk.SourcePollResult
	if err := a.client.Invoke(ctx, pluginsdk.CapabilitySource, pluginsdk.SourceMethodPoll, req, &res); err != nil {
		return nil, fmt.Errorf("plugin source %s: %w", a.id, err)
	}

	items := make([]model.SourceItem, 0, len(res.Items))
	for _, wi := range res.Items {
		if strings.TrimSpace(wi.ID) == "" {
			aplog.Warn("plugin source %s: plugin %s returned an item without id (title %q) — dropped", a.id, a.client.ID(), wi.Title)
			continue
		}
		items = append(items, a.toSourceItem(wi))
	}
	return items, nil
}

func (a *Adapter) toSourceItem(wi pluginsdk.SourceItem) model.SourceItem {
	number := wi.Number
	if number == "" {
		number = wi.ID
	}
	title := wi.Title
	if title == "" {
		title = "plugin item " + wi.ID
	}
	labels := append([]string(nil), wi.Labels...)
	sort.Strings(labels)

	now := time.Now()
	created := parseTime(wi.CreatedAt, now)
	updated := parseTime(wi.UpdatedAt, created)

	return model.SourceItem{
		ID:          wi.ID,
		SourceID:    a.id,
		Number:      number,
		Title:       title,
		Description: wi.Description,
		Labels:      labels,
		Type:        wi.Type,
		Priority:    wi.Priority,
		State:       wi.State,
		URL:         wi.URL,
		Metadata:    wi.Metadata,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}

func parseTime(value string, fallback time.Time) time.Time {
	if value == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fallback
	}
	return t
}

// Acknowledge forwards the dispatch acknowledgement. Failures are returned to
// the caller, which logs them without failing the run — matching how ack
// errors behave for in-tree sources.
func (a *Adapter) Acknowledge(ctx context.Context, cell model.SourceItem, action model.AckAction) error {
	req := pluginsdk.SourceAckRequest{Item: toWireItem(cell), Action: string(action)}
	var res pluginsdk.SourceOKResult
	if err := a.client.Invoke(ctx, pluginsdk.CapabilitySource, pluginsdk.SourceMethodAcknowledge, req, &res); err != nil {
		return fmt.Errorf("plugin source %s: %w", a.id, err)
	}
	return nil
}

// WriteResult forwards the run outcome.
func (a *Adapter) WriteResult(ctx context.Context, cell model.SourceItem, result model.RunResult) error {
	errMsg := ""
	if result.Error != nil {
		errMsg = result.Error.Error()
	}
	req := pluginsdk.SourceWriteResultRequest{
		Item:    toWireItem(cell),
		Success: result.Success,
		Output:  result.Output,
		Error:   errMsg,
	}
	var res pluginsdk.SourceOKResult
	if err := a.client.Invoke(ctx, pluginsdk.CapabilitySource, pluginsdk.SourceMethodWriteResult, req, &res); err != nil {
		return fmt.Errorf("plugin source %s: %w", a.id, err)
	}
	return nil
}

// toWireItem maps the model item back to the SDK wire shape for ack /
// write_result payloads.
func toWireItem(cell model.SourceItem) pluginsdk.SourceItem {
	return pluginsdk.SourceItem{
		ID:          cell.ID,
		Number:      cell.Number,
		Title:       cell.Title,
		Description: cell.Description,
		Labels:      cell.Labels,
		Type:        cell.Type,
		Priority:    cell.Priority,
		State:       cell.State,
		URL:         cell.URL,
		Metadata:    cell.Metadata,
		CreatedAt:   cell.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   cell.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
