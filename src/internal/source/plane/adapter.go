// Package plane provides a Plane source adapter that polls work items
// from a Plane workspace project and maps them to Apiary Cells.
package plane

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
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
	client    *client
	workspace string
	project   string

	// resolved at Connect time
	stateIDToName   map[string]string // state UUID → name
	stateNameToID   map[string]string // lowercase name → UUID
	labelIDToName   map[string]string // label UUID → name
	labelNameToID   map[string]string // lowercase name → UUID
	startedStateID  string            // first state in "started" group

	// filter config
	filterStates []string // lowercase state names
	filterLabels []string // lowercase label names
}

func (a *Adapter) ID() string { return "plane" }

func (a *Adapter) Connect(ctx context.Context, cfg map[string]any) error {
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

	aplog.Info("plane: connecting  workspace=%s  project=%s", workspace, project)

	if err := a.loadStates(ctx); err != nil {
		return err
	}
	if err := a.loadLabels(ctx); err != nil {
		return err
	}

	aplog.Info("plane: ready  states=%d  labels=%d  started-state=%q",
		len(a.stateIDToName), len(a.labelIDToName), a.stateIDToName[a.startedStateID])
	return nil
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
// Called by the dispatcher before the first Poll.
func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(s))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, strings.ToLower(l))
	}
}

func (a *Adapter) Poll(ctx context.Context, since time.Time) ([]model.Cell, error) {
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

// SetState implements source.StateSetter. It transitions the work item to
// the named state, looked up case-insensitively from the project's state list.
func (a *Adapter) SetState(ctx context.Context, cell model.Cell, stateName string) error {
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

// workItemsBase returns the base path for work-items in this project.
func (a *Adapter) workItemsBase() string {
	return fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/work-items", a.workspace, a.project)
}

// toCell maps a Plane work item to a model.Cell.
func (a *Adapter) toCell(item workItem) model.Cell {
	labels := make([]string, 0, len(item.Labels))
	for _, id := range item.Labels {
		if name := a.labelIDToName[id]; name != "" {
			labels = append(labels, name)
		}
	}

	createdAt, _ := time.Parse(time.RFC3339, item.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)

	return model.Cell{
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
}

// formatComment formats a RunResult as HTML for posting to Plane.
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
