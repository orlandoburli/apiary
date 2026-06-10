// Package jira provides a Jira Cloud source adapter that polls issues via
// JQL search and maps them to Apiary source items. Cloud only: Basic auth
// with account email + API token against https://<site>.atlassian.net.
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("jira", func() source.Adapter { return &Adapter{} })
}

// Compile-time checks: the Jira adapter supports the optional source
// capabilities used by the dispatcher and the workflow engine. CI status and
// PR listing are PR-centric and have no Jira mapping, so they are omitted.
var (
	_ source.StateSetter  = (*Adapter)(nil)
	_ source.LabelAdder   = (*Adapter)(nil)
	_ source.LabelRemover = (*Adapter)(nil)
	_ source.TaskPoller   = (*Adapter)(nil)
)

type Adapter struct {
	id      string
	client  *client
	baseURL string // site URL, serves both the REST API and /browse links

	projects     []string // optional project key(s) (e.g. "ERP") to scope polling
	startedState string   // optional status name Acknowledge transitions to

	// Bare JQL datetimes are interpreted in the API user's profile timezone,
	// so it is resolved once (lazily) from /myself before the first search.
	tzOnce sync.Once
	loc    *time.Location

	warnedNoScope bool

	filterStates []string
	filterLabels []string
	jql          string
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetID(id string) { a.id = id }

func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	baseURL, _ := cfg["base_url"].(string)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("jira: config.base_url is required (e.g. \"https://yoursite.atlassian.net\")")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("jira: config.base_url %q must be an absolute http(s) URL", baseURL)
	}
	email, _ := cfg["email"].(string)
	if email == "" {
		return fmt.Errorf("jira: config.email is required (Atlassian account email)")
	}
	apiToken, _ := cfg["api_token"].(string)
	if apiToken == "" {
		return fmt.Errorf("jira: config.api_token is required (create one at https://id.atlassian.com/manage-profile/security/api-tokens)")
	}

	a.projects, err = parseProjects(cfg["project"])
	if err != nil {
		return err
	}
	a.startedState, _ = cfg["started_state"].(string)
	a.baseURL = baseURL
	a.client = newClient(baseURL, email, apiToken)

	aplog.Info("jira: configured  site=%s  projects=%q", baseURL, a.projects)
	return nil
}

// parseProjects accepts config.project as a single key or a list of keys.
func parseProjects(v any) ([]string, error) {
	switch p := v.(type) {
	case nil:
		return nil, nil
	case string:
		if p = strings.TrimSpace(p); p != "" {
			return []string{p}, nil
		}
		return nil, nil
	case []any:
		var keys []string
		for _, e := range p {
			s, ok := e.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("jira: config.project list must contain only non-empty strings, got %v", e)
			}
			keys = append(keys, strings.TrimSpace(s))
		}
		return keys, nil
	default:
		return nil, fmt.Errorf("jira: config.project must be a project key or a list of keys, got %T", v)
	}
}

// SetFilters stores the states/labels filter config (applied client-side).
func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(s))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, strings.ToLower(l))
	}
}

// SetJQL stores the filters.jql clause, ANDed into every search.
func (a *Adapter) SetJQL(jql string) { a.jql = strings.TrimSpace(jql) }

// userLocation resolves the API user's profile timezone exactly once. On any
// failure it falls back to UTC — safe because the JQL `updated >=` clause is
// only a volume reducer; Poll re-applies the precise cut-off client-side.
func (a *Adapter) userLocation(ctx context.Context) *time.Location {
	a.tzOnce.Do(func() {
		a.loc = time.UTC
		data, err := a.client.get(ctx, "/rest/api/3/myself", nil)
		if err != nil {
			aplog.Debug("jira: timezone lookup failed, JQL datetimes use UTC: %v", err)
			return
		}
		var me myself
		if err := json.Unmarshal(data, &me); err != nil || me.TimeZone == "" {
			return
		}
		if loc, err := time.LoadLocation(me.TimeZone); err == nil {
			a.loc = loc
			aplog.Debug("jira: JQL datetimes use API user timezone %s", me.TimeZone)
		}
	})
	return a.loc
}

// buildJQL composes the server-side search filter. Bare JQL datetimes have
// minute granularity and are interpreted in the API user's profile timezone,
// so `since` is converted to loc and padded back by two minutes; Poll
// re-applies the exact cut-off client-side (the binder dedups any overlap).
func buildJQL(projects []string, userJQL string, since time.Time, loc *time.Location) string {
	var clauses []string
	switch len(projects) {
	case 0:
	case 1:
		clauses = append(clauses, fmt.Sprintf("project = %q", projects[0]))
	default:
		quoted := make([]string, len(projects))
		for i, p := range projects {
			quoted[i] = fmt.Sprintf("%q", p)
		}
		clauses = append(clauses, fmt.Sprintf("project in (%s)", strings.Join(quoted, ", ")))
	}
	if q := strings.TrimSpace(userJQL); q != "" {
		clauses = append(clauses, "("+q+")")
	}
	if !since.IsZero() {
		t := since.In(loc).Add(-2 * time.Minute)
		clauses = append(clauses, fmt.Sprintf("updated >= %q", t.Format("2006/01/02 15:04")))
	}
	if len(clauses) == 0 {
		return "ORDER BY updated ASC"
	}
	return strings.Join(clauses, " AND ") + " ORDER BY updated ASC"
}

func (a *Adapter) Poll(ctx context.Context, since time.Time) ([]model.SourceItem, error) {
	if len(a.projects) == 0 && a.jql == "" && !a.warnedNoScope {
		aplog.Info("jira: neither config.project nor filters.jql is set — polling every issue visible to the API user")
		a.warnedNoScope = true
	}

	jql := buildJQL(a.projects, a.jql, since, a.userLocation(ctx))
	issues, err := a.client.searchAll(ctx, jql)
	if err != nil {
		return nil, fmt.Errorf("jira: polling issues: %w", err)
	}

	var cells []model.SourceItem
	for _, item := range issues {
		if !a.matchesFilters(item) {
			continue
		}
		cell := a.toSourceItem(item)
		if !since.IsZero() && !cell.UpdatedAt.After(since) {
			continue
		}
		cells = append(cells, cell)
	}
	return cells, nil
}

func (a *Adapter) matchesFilters(item issue) bool {
	if len(a.filterStates) > 0 {
		stateName := ""
		if item.Fields.Status != nil {
			stateName = strings.ToLower(item.Fields.Status.Name)
		}
		if !containsAny(a.filterStates, stateName) {
			aplog.Debug("  issue %s (%q): state %q not in filter %v", item.Key, item.Fields.Summary, stateName, a.filterStates)
			return false
		}
	}
	if len(a.filterLabels) > 0 {
		itemLabels := make([]string, 0, len(item.Fields.Labels))
		for _, l := range item.Fields.Labels {
			itemLabels = append(itemLabels, strings.ToLower(l))
		}
		for _, required := range a.filterLabels {
			if !containsAny(itemLabels, required) {
				aplog.Debug("  issue %s (%q): missing required label %q (has: %v)", item.Key, item.Fields.Summary, required, itemLabels)
				return false
			}
		}
	}
	return true
}

// Acknowledge transitions the issue into work when it is dispatched: to the
// configured started_state if set, otherwise to the first transition whose
// target is in Jira's "In Progress" category (statusCategory "indeterminate").
func (a *Adapter) Acknowledge(ctx context.Context, cell model.SourceItem, action model.AckAction) error {
	if action != model.AckActionInProgress {
		return nil
	}
	if a.startedState != "" {
		if err := a.transitionTo(ctx, cell.ID, a.startedState); err != nil {
			return fmt.Errorf("jira: acknowledging %s: %w", cell.ID, err)
		}
		return nil
	}

	transitions, err := a.listTransitions(ctx, cell.ID)
	if err != nil {
		return fmt.Errorf("jira: acknowledging %s: %w", cell.ID, err)
	}
	for _, t := range transitions {
		if t.To.StatusCategory.Key == "indeterminate" {
			if err := a.applyTransition(ctx, cell.ID, t.ID); err != nil {
				return fmt.Errorf("jira: acknowledging %s: %w", cell.ID, err)
			}
			return nil
		}
	}

	// No in-progress transition available: if the issue already sits in an
	// in-progress status this is a no-op, otherwise the workflow has no way in.
	current, err := a.currentStatus(ctx, cell.ID)
	if err == nil && current.StatusCategory.Key == "indeterminate" {
		return nil
	}
	return fmt.Errorf("jira: acknowledging %s: no transition to an in-progress status (available: %s) — set config.started_state or adjust the Jira workflow",
		cell.ID, availableTargets(transitions))
}

func (a *Adapter) WriteResult(ctx context.Context, cell model.SourceItem, result model.RunResult) error {
	path := "/rest/api/3/issue/" + url.PathEscape(cell.ID) + "/comment"
	if _, err := a.client.post(ctx, path, commentCreateRequest{Body: formatComment(result)}); err != nil {
		return fmt.Errorf("jira: writing result to %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WebhookHandler() http.Handler { return nil }

// PollTask fetches the current state of a single issue plus its comments, for
// the workflow engine to evaluate approval-step conditions. Implements
// source.TaskPoller.
func (a *Adapter) PollTask(ctx context.Context, cellID string) (model.SourceItem, error) {
	path := "/rest/api/3/issue/" + url.PathEscape(cellID)
	data, err := a.client.get(ctx, path, url.Values{"fields": {searchFields}})
	if err != nil {
		return model.SourceItem{}, fmt.Errorf("jira: poll task %s: %w", cellID, err)
	}
	var item issue
	if err := json.Unmarshal(data, &item); err != nil {
		return model.SourceItem{}, fmt.Errorf("jira: decoding issue %s: %w", cellID, err)
	}

	cell := a.toSourceItem(item)
	comments, err := a.fetchComments(ctx, cellID)
	if err != nil {
		aplog.Debug("jira: fetch comments for %s: %v", cellID, err)
	} else {
		cell.Comments = comments
	}
	return cell, nil
}

// fetchComments retrieves every comment on an issue, oldest first, flattening
// the ADF bodies to plain text.
func (a *Adapter) fetchComments(ctx context.Context, cellID string) ([]model.Comment, error) {
	var out []model.Comment
	startAt := 0

	for {
		params := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {"100"},
			"orderBy":    {"created"},
		}
		path := "/rest/api/3/issue/" + url.PathEscape(cellID) + "/comment"
		data, err := a.client.get(ctx, path, params)
		if err != nil {
			return nil, err
		}
		var pg commentPage
		if err := json.Unmarshal(data, &pg); err != nil {
			return nil, fmt.Errorf("jira: decoding comments for %s: %w", cellID, err)
		}
		for _, c := range pg.Comments {
			out = append(out, model.Comment{
				ID:        c.ID,
				Body:      adfToText(c.Body),
				CreatedAt: parseJiraTime(c.Created),
			})
		}
		startAt += len(pg.Comments)
		if len(pg.Comments) == 0 || startAt >= pg.Total {
			return out, nil
		}
	}
}

// SetState transitions the issue to the named status. Implements
// source.StateSetter.
func (a *Adapter) SetState(ctx context.Context, cell model.SourceItem, stateName string) error {
	if err := a.transitionTo(ctx, cell.ID, stateName); err != nil {
		return fmt.Errorf("jira: setting state %q on %s: %w", stateName, cell.ID, err)
	}
	return nil
}

// transitionTo moves an issue to the named status via the transitions API.
// Jira only exposes transitions valid from the issue's current status, so a
// missing match is either "already there" (success) or a workflow gap (error
// listing what was available, to make misconfiguration debuggable).
func (a *Adapter) transitionTo(ctx context.Context, cellID, target string) error {
	transitions, err := a.listTransitions(ctx, cellID)
	if err != nil {
		return err
	}
	if t, ok := matchTransition(transitions, target); ok {
		return a.applyTransition(ctx, cellID, t.ID)
	}

	current, err := a.currentStatus(ctx, cellID)
	if err == nil && strings.EqualFold(current.Name, target) {
		return nil
	}
	return fmt.Errorf("no transition to state %q from current status %q (available: %s)",
		target, current.Name, availableTargets(transitions))
}

func (a *Adapter) listTransitions(ctx context.Context, cellID string) ([]transition, error) {
	path := "/rest/api/3/issue/" + url.PathEscape(cellID) + "/transitions"
	data, err := a.client.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	var resp transitionsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("jira: decoding transitions for %s: %w", cellID, err)
	}
	return resp.Transitions, nil
}

func (a *Adapter) applyTransition(ctx context.Context, cellID, transitionID_ string) error {
	path := "/rest/api/3/issue/" + url.PathEscape(cellID) + "/transitions"
	_, err := a.client.post(ctx, path, transitionRequest{Transition: transitionID{ID: transitionID_}})
	return err
}

func (a *Adapter) currentStatus(ctx context.Context, cellID string) (statusEntity, error) {
	path := "/rest/api/3/issue/" + url.PathEscape(cellID)
	data, err := a.client.get(ctx, path, url.Values{"fields": {"status"}})
	if err != nil {
		return statusEntity{}, err
	}
	var item issue
	if err := json.Unmarshal(data, &item); err != nil {
		return statusEntity{}, fmt.Errorf("jira: decoding issue %s: %w", cellID, err)
	}
	if item.Fields.Status == nil {
		return statusEntity{}, fmt.Errorf("jira: issue %s has no status", cellID)
	}
	return *item.Fields.Status, nil
}

// matchTransition finds the transition whose target status matches, falling
// back to the transition's own name. Both are case-insensitive.
func matchTransition(transitions []transition, target string) (transition, bool) {
	for _, t := range transitions {
		if strings.EqualFold(t.To.Name, target) {
			return t, true
		}
	}
	for _, t := range transitions {
		if strings.EqualFold(t.Name, target) {
			return t, true
		}
	}
	return transition{}, false
}

func availableTargets(transitions []transition) string {
	if len(transitions) == 0 {
		return "none"
	}
	names := make([]string, 0, len(transitions))
	for _, t := range transitions {
		names = append(names, t.To.Name)
	}
	return strings.Join(names, ", ")
}

// AddLabels adds the named labels to the issue with Jira's atomic update
// verbs. Labels are free-form in Jira (created implicitly), but may not
// contain whitespace — offending names are rewritten with "-". Implements
// source.LabelAdder.
func (a *Adapter) AddLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ops := make([]labelOp, 0, len(names))
	for _, n := range names {
		ops = append(ops, labelOp{Add: sanitizeLabel(n)})
	}
	path := "/rest/api/3/issue/" + url.PathEscape(cell.ID)
	if _, err := a.client.put(ctx, path, labelsUpdateRequest{Update: labelsUpdate{Labels: ops}}); err != nil {
		return fmt.Errorf("jira: adding labels %v to %s: %w", names, cell.ID, err)
	}
	return nil
}

// RemoveLabels removes the named labels from the issue; removing an absent
// label is a server-side no-op. Implements source.LabelRemover.
func (a *Adapter) RemoveLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ops := make([]labelOp, 0, len(names))
	for _, n := range names {
		ops = append(ops, labelOp{Remove: sanitizeLabel(n)})
	}
	path := "/rest/api/3/issue/" + url.PathEscape(cell.ID)
	if _, err := a.client.put(ctx, path, labelsUpdateRequest{Update: labelsUpdate{Labels: ops}}); err != nil {
		return fmt.Errorf("jira: removing labels %v from %s: %w", names, cell.ID, err)
	}
	return nil
}

var labelWhitespaceRe = regexp.MustCompile(`\s+`)

// sanitizeLabel rewrites whitespace to "-": Jira rejects the whole update
// when any label contains spaces.
func sanitizeLabel(name string) string {
	clean := labelWhitespaceRe.ReplaceAllString(strings.TrimSpace(name), "-")
	if clean != name {
		aplog.Debug("jira: label %q rewritten to %q (Jira labels cannot contain whitespace)", name, clean)
	}
	return clean
}

// jiraTimeLayout is Jira's timestamp format: RFC3339-like but with a colonless
// zone offset, so time.RFC3339 fails to parse it.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

func parseJiraTime(s string) time.Time {
	if t, err := time.Parse(jiraTimeLayout, s); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func (a *Adapter) toSourceItem(item issue) model.SourceItem {
	labels := make([]string, 0, len(item.Fields.Labels))
	for _, l := range item.Fields.Labels {
		labels = append(labels, strings.ToLower(l))
	}

	var state, priority, issueType string
	if item.Fields.Status != nil {
		state = item.Fields.Status.Name
	}
	if item.Fields.Priority != nil {
		priority = strings.ToLower(item.Fields.Priority.Name)
	}
	if item.Fields.IssueType != nil {
		issueType = strings.ToLower(item.Fields.IssueType.Name)
	}

	return model.SourceItem{
		// The numeric id is the binding identity: issue keys change when an
		// issue moves projects, ids never do. Every /issue/{idOrKey} endpoint
		// accepts the id, so write-backs receive it transparently.
		ID:          item.ID,
		SourceID:    a.ID(),
		Number:      item.Key,
		Title:       item.Fields.Summary,
		Description: adfToText(item.Fields.Description),
		Labels:      labels,
		Type:        issueType,
		Priority:    priority,
		State:       state,
		URL:         a.baseURL + "/browse/" + item.Key,
		CreatedAt:   parseJiraTime(item.Fields.Created),
		UpdatedAt:   parseJiraTime(item.Fields.Updated),
	}
}

func containsAny(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
