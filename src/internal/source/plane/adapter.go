// Package plane provides a Plane source adapter that polls work items
// from a Plane workspace project and maps them to Apiary Cells.
package plane

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("plane", func() source.Adapter { return &Adapter{} })
}

// Adapter implements source.Adapter for Plane.so.
type Adapter struct {
	id        string // source ID from config
	client    *client
	workspace string
	project   string

	// lazily loaded on first Poll()
	metaOnce       sync.Once
	metaErr        error
	stateIDToName  map[string]string
	stateNameToID  map[string]string
	labelIDToName  map[string]string
	labelNameToID  map[string]string
	startedStateID string
	issuesPath     string // resolved on first poll: "work-items" or "issues"

	// filter config
	filterStates []string
	filterLabels []string
}

func (a *Adapter) ID() string { return a.id }

// SetID sets the source ID for this adapter.
func (a *Adapter) SetID(id string) { a.id = id }

// Connect validates config and creates the HTTP client.
// Metadata (states, labels) is loaded lazily on first Poll to avoid
// hitting the rate limit at startup.
func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	apiKey, _ := cfg["api_key"].(string)
	if apiKey == "" {
		return fmt.Errorf("plane: config.api_key is required")
	}
	workspace, _ := cfg["workspace"].(string)
	if workspace == "" {
		return fmt.Errorf("plane: config.workspace is required")
	}
	project, _ := cfg["project"].(string)
	if project == "" {
		return fmt.Errorf("plane: config.project is required")
	}

	baseURL, _ := cfg["base_url"].(string)

	a.workspace = workspace
	a.project = project
	a.client = newClient(baseURL, apiKey)

	host := baseURL
	if host == "" {
		host = defaultBaseURL
	}
	aplog.Info("plane: configured  host=%s  workspace=%s  project=%s  (metadata loads on first poll)",
		host, workspace, project)
	return nil
}

// loadMetadata fetches states and labels exactly once.
// A 500ms pause between the two requests avoids back-to-back rate limit hits.
func (a *Adapter) loadMetadata(ctx context.Context) error {
	a.metaOnce.Do(func() {
		aplog.Info("plane: loading metadata (states + labels)…")
		if err := a.loadStates(ctx); err != nil {
			a.metaErr = err
			return
		}
		// brief pause so we don't fire two requests in the same rate-limit window
		select {
		case <-ctx.Done():
			a.metaErr = ctx.Err()
			return
		case <-time.After(500 * time.Millisecond):
		}
		if err := a.loadLabels(ctx); err != nil {
			a.metaErr = err
			return
		}
		a.issuesPath = a.resolveIssuesPath(ctx)
		aplog.Info("plane: ready  states=%d  labels=%d  started-state=%q  endpoint=%s",
			len(a.stateIDToName), len(a.labelIDToName), a.stateIDToName[a.startedStateID], a.issuesPath)
	})
	return a.metaErr
}

// resolveIssuesPath detects whether this Plane instance uses the newer
// "work-items" endpoint or the legacy "issues" endpoint.
func (a *Adapter) resolveIssuesPath(ctx context.Context) string {
	probe := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/work-items/?per_page=1", a.workspace, a.project)
	_, err := a.client.getNoLog(ctx, probe)
	if err == nil {
		aplog.Debug("plane: endpoint=work-items")
		return "work-items"
	}
	aplog.Debug("plane: endpoint=issues (work-items not available on this version)")
	return "issues"
}

func (a *Adapter) loadStates(ctx context.Context) error {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/states/", a.workspace, a.project)
	aplog.Debug("plane: fetching states from %s", path)

	states, err := getAll[state](ctx, a.client, path)
	if err != nil {
		return fmt.Errorf("plane: loading states: %w", err)
	}

	a.stateIDToName = make(map[string]string, len(states))
	a.stateNameToID = make(map[string]string, len(states))
	for _, s := range states {
		a.stateIDToName[s.ID] = s.Name
		a.stateNameToID[strings.ToLower(s.Name)] = s.ID
		if s.Group == "started" && a.startedStateID == "" {
			a.startedStateID = s.ID
		}
	}
	return nil
}

func (a *Adapter) loadLabels(ctx context.Context) error {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/labels/", a.workspace, a.project)
	aplog.Debug("plane: fetching labels from %s", path)

	labels, err := getAll[label](ctx, a.client, path)
	if err != nil {
		return fmt.Errorf("plane: loading labels: %w", err)
	}

	a.labelIDToName = make(map[string]string, len(labels))
	a.labelNameToID = make(map[string]string, len(labels))
	for _, l := range labels {
		a.labelIDToName[l.ID] = l.Name
		a.labelNameToID[strings.ToLower(l.Name)] = l.ID
	}
	return nil
}

// SetFilters stores the filter config passed from the source config.
func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(s))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, strings.ToLower(l))
	}
}

func (a *Adapter) Poll(ctx context.Context, since time.Time) ([]model.Cell, error) {
	if err := a.loadMetadata(ctx); err != nil {
		return nil, err
	}

	path := a.workItemsBase() + "/"
	items, err := getAll[workItem](ctx, a.client, path)
	if err != nil {
		return nil, fmt.Errorf("plane: polling work items: %w", err)
	}

	var cells []model.Cell
	for _, item := range items {
		if !a.matchesFilters(item) {
			continue
		}
		updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)
		if !since.IsZero() && !updatedAt.After(since) {
			continue
		}
		cells = append(cells, a.toCell(item))
	}
	return cells, nil
}

func (a *Adapter) matchesFilters(item workItem) bool {
	if len(a.filterStates) > 0 {
		stateName := strings.ToLower(a.stateIDToName[item.State])
		if !containsAny(a.filterStates, stateName) {
			aplog.Debug("  item %s (%q): state %q not in filter %v", item.ID, item.Name, stateName, a.filterStates)
			return false
		}
	}
	if len(a.filterLabels) > 0 {
		itemLabels := make([]string, 0, len(item.Labels))
		for _, id := range item.Labels {
			itemLabels = append(itemLabels, strings.ToLower(a.labelIDToName[id]))
		}
		for _, required := range a.filterLabels {
			if !containsAny(itemLabels, required) {
				aplog.Debug("  item %s (%q): missing required label %q (has: %v)", item.ID, item.Name, required, itemLabels)
				return false
			}
		}
	}
	return true
}

func (a *Adapter) Acknowledge(ctx context.Context, cell model.Cell, action model.AckAction) error {
	if action != model.AckActionInProgress {
		return nil
	}
	if a.startedStateID == "" {
		return fmt.Errorf("plane: no 'started' state found in project — cannot acknowledge")
	}
	path := a.workItemsBase() + "/" + cell.ID + "/"
	_, err := a.client.patch(ctx, path, patchRequest{State: a.startedStateID})
	if err != nil {
		return fmt.Errorf("plane: acknowledging %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WriteResult(ctx context.Context, cell model.Cell, result model.RunResult) error {
	path := a.workItemsBase() + "/" + cell.ID + "/comments/"
	comment := formatComment(result)
	_, err := a.client.post(ctx, path, commentRequest{CommentHTML: comment})
	if err != nil {
		return fmt.Errorf("plane: writing result to %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WebhookHandler() http.Handler { return nil }

// SetState implements source.StateSetter.
func (a *Adapter) SetState(ctx context.Context, cell model.Cell, stateName string) error {
	if err := a.loadMetadata(ctx); err != nil {
		return err
	}
	stateID, ok := a.stateNameToID[strings.ToLower(stateName)]
	if !ok {
		return fmt.Errorf("plane: state %q not found in project", stateName)
	}
	path := a.workItemsBase() + "/" + cell.ID + "/"
	_, err := a.client.patch(ctx, path, patchRequest{State: stateID})
	if err != nil {
		return fmt.Errorf("plane: setting state %q on %s: %w", stateName, cell.ID, err)
	}
	return nil
}

func (a *Adapter) workItemsBase() string {
	path := a.issuesPath
	if path == "" {
		path = "work-items" // default before first poll
	}
	return fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/%s", a.workspace, a.project, path)
}

func (a *Adapter) toCell(item workItem) model.Cell {
	labels := make([]string, 0, len(item.Labels))
	for _, id := range item.Labels {
		if name := a.labelIDToName[id]; name != "" {
			labels = append(labels, strings.ToLower(name))
		}
	}
	createdAt, _ := time.Parse(time.RFC3339, item.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)

	cell := model.Cell{
		ID:          item.ID,
		SourceID:    a.ID(),
		Title:       item.Name,
		Description: item.DescriptionStripped,
		Labels:      labels,
		Priority:    item.Priority,
		State:       a.stateIDToName[item.State],
		URL:         fmt.Sprintf("https://app.plane.so/%s/projects/%s/work-items/%d/", a.workspace, a.project, item.SequenceID),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}

	if len(labels) > 0 {
		aplog.Debug("item %q: labels=%v", item.Name, labels)
	}

	return cell
}

func formatComment(result model.RunResult) string {
	var b strings.Builder
	if result.Success {
		b.WriteString("<p>✓ <strong>Apiary run complete</strong>")
	} else {
		b.WriteString("<p>✗ <strong>Apiary run failed</strong>")
	}
	b.WriteString(fmt.Sprintf(" · worker: <code>%s</code>", html.EscapeString(result.WorkerID)))
	b.WriteString(fmt.Sprintf(" · duration: %s</p>", result.Duration.Round(time.Second)))
	if result.Output != "" {
		b.WriteString("<pre>")
		b.WriteString(html.EscapeString(result.Output))
		b.WriteString("</pre>")
	}
	if result.Error != nil {
		b.WriteString("<p><strong>Error:</strong> <code>")
		b.WriteString(html.EscapeString(result.Error.Error()))
		b.WriteString("</code></p>")
	}
	return b.String()
}

func containsAny(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
