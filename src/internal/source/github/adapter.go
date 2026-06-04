package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("github", func() source.Adapter { return &Adapter{} })
}

type Adapter struct {
	id         string
	client     *client
	owner      string
	repo       string
	webBaseURL string

	filterStates []string
	filterLabels []string
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetID(id string) { a.id = id }

func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	repoStr, _ := cfg["repo"].(string)
	if repoStr == "" {
		return fmt.Errorf("github: config.repo is required (e.g. \"owner/repo\")")
	}

	parts := strings.SplitN(repoStr, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("github: config.repo %q must be in \"owner/repo\" format", repoStr)
	}

	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)

	a.owner = parts[0]
	a.repo = parts[1]
	a.client = newClient(baseURL, apiKey)

	if baseURL == "" || baseURL == defaultBaseURL {
		a.webBaseURL = "https://github.com"
	} else {
		u, err := url.Parse(baseURL)
		if err != nil {
			a.webBaseURL = "https://github.com"
		} else {
			a.webBaseURL = u.Scheme + "://" + u.Host
		}
	}

	aplog.Info("github: configured  repo=%s/%s  host=%s", a.owner, a.repo, a.webBaseURL)
	return nil
}

func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(s))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, strings.ToLower(l))
	}
}

func (a *Adapter) Poll(ctx context.Context, since time.Time) ([]model.Cell, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", a.owner, a.repo)
	params := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
	}
	if !since.IsZero() {
		params.Set("since", since.Format(time.RFC3339))
	}

	issues, err := a.client.getAllIssues(ctx, path, params)
	if err != nil {
		return nil, fmt.Errorf("github: polling issues: %w", err)
	}

	var cells []model.Cell
	for _, item := range issues {
		if item.PullRequest != nil {
			continue
		}
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

func (a *Adapter) matchesFilters(item issue) bool {
	if len(a.filterStates) > 0 {
		stateName := strings.ToLower(item.State)
		if !containsAny(a.filterStates, stateName) {
			aplog.Debug("  issue #%d (%q): state %q not in filter %v", item.Number, item.Title, stateName, a.filterStates)
			return false
		}
	}
	if len(a.filterLabels) > 0 {
		itemLabels := make([]string, 0, len(item.Labels))
		for _, l := range item.Labels {
			itemLabels = append(itemLabels, strings.ToLower(l.Name))
		}
		for _, required := range a.filterLabels {
			if !containsAny(itemLabels, required) {
				aplog.Debug("  issue #%d (%q): missing required label %q (has: %v)", item.Number, item.Title, required, itemLabels)
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
	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, issueNo)
	_, err := a.client.patch(ctx, path, issueRequest{Labels: []string{"in-progress"}})
	if err != nil {
		return fmt.Errorf("github: acknowledging %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WriteResult(ctx context.Context, cell model.Cell, result model.RunResult) error {
	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/comments", a.owner, a.repo, issueNo)
	body := formatComment(result)
	_, err := a.client.post(ctx, path, commentRequest{Body: body})
	if err != nil {
		return fmt.Errorf("github: writing result to %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WebhookHandler() http.Handler { return nil }

func (a *Adapter) SetState(ctx context.Context, cell model.Cell, stateName string) error {
	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, issueNo)
	_, err := a.client.patch(ctx, path, issueRequest{State: stateName})
	if err != nil {
		return fmt.Errorf("github: setting state %q on %s: %w", stateName, cell.ID, err)
	}
	return nil
}

func (a *Adapter) AddLabels(ctx context.Context, cell model.Cell, names []string) error {
	if len(names) == 0 {
		return nil
	}

	idSet := make(map[string]struct{})
	for _, n := range cell.Labels {
		idSet[strings.ToLower(n)] = struct{}{}
	}
	for _, n := range names {
		if err := a.ensureLabel(ctx, n); err != nil {
			return err
		}
		idSet[strings.ToLower(n)] = struct{}{}
	}

	labelList := make([]string, 0, len(idSet))
	for n := range idSet {
		labelList = append(labelList, n)
	}

	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, issueNo)
	_, err := a.client.patch(ctx, path, issueRequest{Labels: labelList})
	if err != nil {
		return fmt.Errorf("github: adding labels %v to %s: %w", names, cell.ID, err)
	}
	return nil
}

func (a *Adapter) ensureLabel(ctx context.Context, name string) error {
	path := fmt.Sprintf("/repos/%s/%s/labels", a.owner, a.repo)
	_, err := a.client.post(ctx, path, labelCreateRequest{Name: name})
	if err != nil && !strings.Contains(err.Error(), "status 422") {
		return fmt.Errorf("github: ensuring label %q: %w", name, err)
	}
	return nil
}

func (a *Adapter) toCell(item issue) model.Cell {
	labels := make([]string, 0, len(item.Labels))
	for _, l := range item.Labels {
		labels = append(labels, strings.ToLower(l.Name))
	}
	createdAt, _ := time.Parse(time.RFC3339, item.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)

	return model.Cell{
		ID:          fmt.Sprintf("%d", item.Number),
		SourceID:    a.ID(),
		Number:      fmt.Sprintf("#%d", item.Number),
		Title:       item.Title,
		Description: item.Body,
		Labels:      labels,
		State:       item.State,
		URL:         item.HTMLURL,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func formatComment(result model.RunResult) string {
	var b strings.Builder
	if result.Success {
		b.WriteString("✓ **Apiary run complete**")
	} else {
		b.WriteString("✗ **Apiary run failed**")
	}
	b.WriteString(fmt.Sprintf(" · worker: `%s`", result.WorkerID))
	b.WriteString(fmt.Sprintf(" · duration: %s", result.Duration.Round(time.Second)))

	if result.Output != "" {
		b.WriteString("\n\n```\n")
		b.WriteString(result.Output)
		b.WriteString("\n```")
	}
	if result.Error != nil {
		b.WriteString(fmt.Sprintf("\n\n**Error:** `%s`", result.Error.Error()))
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
