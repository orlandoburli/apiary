// Package codeberg implements a source.Adapter for Codeberg and any other
// Forgejo/Gitea instance. The Forgejo REST API closely mirrors GitHub's, so the
// shape follows the github adapter; the differences are concentrated here:
// "Authorization: token" auth, label operations by id (not name), commit
// statuses instead of check-runs, a bool Mergeable flag instead of
// mergeable_state, native issue dependencies, and no sub-issue API.
package codeberg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("codeberg", func() source.Adapter { return &Adapter{} })
}

// Compile-time checks for the optional capabilities Codeberg supports. Note the
// absence of source.SubIssueCreator: Forgejo/Gitea has no native parent/child
// issue API, so spawned tasks cannot be materialized as sub-issues here.
var (
	_ source.StateSetter       = (*Adapter)(nil)
	_ source.LabelAdder        = (*Adapter)(nil)
	_ source.LabelRemover      = (*Adapter)(nil)
	_ source.TaskPoller        = (*Adapter)(nil)
	_ source.CIStatusPoller    = (*Adapter)(nil)
	_ source.PullRequestLister = (*Adapter)(nil)
	_ source.BlockerLister     = (*Adapter)(nil)
)

type Adapter struct {
	id         string
	client     *client
	owner      string
	repo       string
	webBaseURL string

	filterStates []string
	filterLabels []string

	// labelByName caches repo labels (lowercased name → id). Forgejo's label
	// endpoints work in ids, so every add/remove resolves a name through here.
	mu           sync.Mutex
	labelByName  map[string]int64
	labelsLoaded bool
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetID(id string) { a.id = id }

func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	repoStr, _ := cfg["repo"].(string)
	if repoStr == "" {
		return fmt.Errorf("codeberg: config.repo is required (e.g. \"owner/repo\")")
	}
	parts := strings.SplitN(repoStr, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("codeberg: config.repo %q must be in \"owner/repo\" format", repoStr)
	}

	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)

	a.owner = parts[0]
	a.repo = parts[1]
	a.client = newClient(baseURL, apiKey)
	a.labelByName = map[string]int64{}

	// Derive the browser host from the API base. Codeberg's API lives at
	// codeberg.org/api/v1, so strip the /api/v1 suffix to reach the web host.
	a.webBaseURL = deriveWebBaseURL(baseURL)

	aplog.Info("codeberg: configured  repo=%s/%s  host=%s", a.owner, a.repo, a.webBaseURL)
	return nil
}

// deriveWebBaseURL turns an API base URL into the browser-facing host root.
func deriveWebBaseURL(baseURL string) string {
	if baseURL == "" || baseURL == defaultBaseURL {
		return "https://codeberg.org"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "https://codeberg.org"
	}
	return u.Scheme + "://" + u.Host
}

func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(s))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, strings.ToLower(l))
	}
}

func (a *Adapter) Poll(ctx context.Context, _ time.Time) ([]model.SourceItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", a.owner, a.repo)

	state := "open"
	if len(a.filterStates) > 0 {
		state = a.filterStates[0]
	}
	// type=issues excludes pull requests server-side; the PullRequest guard
	// below is a belt-and-suspenders check for older Forgejo versions.
	params := url.Values{
		"type":  {"issues"},
		"state": {state},
	}

	issues, err := a.client.getAllIssues(ctx, path, params)
	if err != nil {
		return nil, fmt.Errorf("codeberg: polling issues: %w", err)
	}

	var cells []model.SourceItem
	for _, item := range issues {
		if item.PullRequest != nil {
			aplog.Debug("  #%d (%q): pull request, skipping", item.Number, item.Title)
			continue
		}
		if !a.matchesFilters(item) {
			continue
		}
		cells = append(cells, a.toSourceItem(item))
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

func (a *Adapter) Acknowledge(ctx context.Context, cell model.SourceItem, action model.AckAction) error {
	if action != model.AckActionInProgress {
		return nil
	}
	if err := a.AddLabels(ctx, cell, []string{"in-progress"}); err != nil {
		return fmt.Errorf("codeberg: acknowledging %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WriteResult(ctx context.Context, cell model.SourceItem, result model.RunResult) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/comments", a.owner, a.repo, cell.ID)
	_, err := a.client.post(ctx, path, commentRequest{Body: formatComment(result)})
	if err != nil {
		return fmt.Errorf("codeberg: writing result to %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WebhookHandler() http.Handler { return nil }

// PollTask fetches a single issue plus its comments for approval-step
// evaluation. Implements source.TaskPoller.
func (a *Adapter) PollTask(ctx context.Context, cellID string) (model.SourceItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return model.SourceItem{}, fmt.Errorf("codeberg: poll task %s: %w", cellID, err)
	}
	var item issue
	if err := json.Unmarshal(body, &item); err != nil {
		return model.SourceItem{}, fmt.Errorf("codeberg: decoding issue %s: %w", cellID, err)
	}

	cell := a.toSourceItem(item)
	comments, err := a.fetchComments(ctx, cellID)
	if err != nil {
		aplog.Debug("codeberg: fetch comments for %s: %v", cellID, err)
	} else {
		cell.Comments = comments
	}
	return cell, nil
}

func (a *Adapter) fetchComments(ctx context.Context, issueNo string) ([]model.Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/comments", a.owner, a.repo, issueNo)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var raw []comment
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("codeberg: decoding comments for %s: %w", issueNo, err)
	}
	out := make([]model.Comment, 0, len(raw))
	for _, c := range raw {
		created, _ := time.Parse(time.RFC3339, c.CreatedAt)
		out = append(out, model.Comment{ID: fmt.Sprintf("%d", c.ID), Body: c.Body, CreatedAt: created})
	}
	return out, nil
}

func (a *Adapter) SetState(ctx context.Context, cell model.SourceItem, stateName string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, cell.ID)
	_, err := a.client.patch(ctx, path, editIssueRequest{State: stateName})
	if err != nil {
		return fmt.Errorf("codeberg: setting state %q on %s: %w", stateName, cell.ID, err)
	}
	return nil
}

// AddLabels resolves each label name to its id (creating the label if needed)
// and POSTs them to the issue, which Forgejo merges into the existing set.
// Implements source.LabelAdder.
func (a *Adapter) AddLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ids, err := a.resolveLabelIDs(ctx, names, true)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/labels", a.owner, a.repo, cell.ID)
	if _, err := a.client.post(ctx, path, issueLabelsRequest{Labels: ids}); err != nil {
		return fmt.Errorf("codeberg: adding labels %v to %s: %w", names, cell.ID, err)
	}
	return nil
}

// RemoveLabels deletes the named labels from an issue, one DELETE per label by
// its id so the rest are untouched. A label that does not exist on the repo (or
// is already absent from the issue → 404) is skipped. Implements
// source.LabelRemover.
func (a *Adapter) RemoveLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	ids, err := a.resolveLabelIDs(ctx, names, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		path := fmt.Sprintf("/repos/%s/%s/issues/%s/labels/%d", a.owner, a.repo, cell.ID, id)
		if _, err := a.client.delete(ctx, path); err != nil {
			if strings.Contains(err.Error(), "status 404") {
				continue // label already absent from the issue
			}
			return fmt.Errorf("codeberg: removing label id %d from %s: %w", id, cell.ID, err)
		}
	}
	return nil
}

// PollCIStatus reports the CI status of the pull request linked to the issue.
// Forgejo has no check-runs API, so it aggregates the combined commit status of
// the PR's head commit (where Forgejo Actions and external CI both report).
// Implements source.CIStatusPoller.
func (a *Adapter) PollCIStatus(ctx context.Context, cellID string) (source.CIStatus, error) {
	prNumber, err := a.findPRNumber(ctx, cellID)
	if err != nil {
		if isAuthError(err) {
			return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: cannot read PRs for issue %s — the configured token likely lacks repository read access: %w", cellID, err)
		}
		return source.CIStatus{Status: "pending"}, nil // no timeline access; keep waiting
	}
	if prNumber == 0 {
		return source.CIStatus{Status: "pending"}, nil // no PR yet
	}

	prBody, err := a.client.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, prNumber))
	if err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: fetching PR #%d for issue %s: %w", prNumber, cellID, err)
	}
	var pr pullRequest
	if err := json.Unmarshal(prBody, &pr); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: decoding PR %d: %w", prNumber, err)
	}

	// An unmerged PR that Forgejo reports as not mergeable has conflicts and can
	// never merge until rebased/resolved, so waiting for CI on it is pointless —
	// surface the conflict so the wait_for step hands it back to the engineer.
	if !pr.Merged && !pr.Mergeable {
		aplog.Info("codeberg: PR #%d for %s is not mergeable (conflict)", pr.Number, cellID)
		return source.CIStatus{Status: "conflict", URL: prWebURL(pr, a.webBaseURL, a.owner, a.repo)}, nil
	}

	headSHA := pr.Head.SHA
	if headSHA == "" {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: PR #%d has no head SHA", prNumber)
	}

	statusBody, err := a.client.get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s/status", a.owner, a.repo, headSHA))
	if err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: fetching status for %s: %w", headSHA, err)
	}
	var combined combinedStatus
	if err := json.Unmarshal(statusBody, &combined); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("codeberg: decoding commit status: %w", err)
	}

	// Aggregate every per-context status. No statuses at all means CI has not
	// started yet → pending (never report passed on an empty set).
	anyFail, anyPending, anySignal := false, false, false
	checks := make([]struct {
		Name   string
		Status string
	}, 0, len(combined.Statuses))
	for _, st := range combined.Statuses {
		anySignal = true
		norm := normalizeStatus(st.Status)
		switch norm {
		case "failed":
			anyFail = true
		case "pending":
			anyPending = true
		}
		checks = append(checks, struct {
			Name   string
			Status string
		}{Name: st.Context, Status: norm})
	}

	overall := "pending"
	switch {
	case anyFail:
		overall = "failed"
	case anyPending:
		overall = "pending"
	case anySignal:
		overall = "passed"
	}
	return source.CIStatus{Status: overall, URL: prWebURL(pr, a.webBaseURL, a.owner, a.repo), Checks: checks}, nil
}

// ListPullRequests returns every pull request that references the issue, oldest
// first, derived from the issue timeline. Implements source.PullRequestLister.
func (a *Adapter) ListPullRequests(ctx context.Context, cellID string) ([]source.PullRequestRef, error) {
	timeline, err := a.fetchTimeline(ctx, cellID)
	if err != nil {
		return nil, err
	}
	var prs []source.PullRequestRef
	seen := make(map[int]bool)
	for _, e := range timeline {
		if e.RefIssue == nil || e.RefIssue.PullRequest == nil {
			continue
		}
		n := e.RefIssue.Number
		if n == 0 || seen[n] {
			continue
		}
		seen[n] = true
		prs = append(prs, source.PullRequestRef{
			Number: n,
			URL:    fmt.Sprintf("%s/%s/%s/pulls/%d", a.webBaseURL, a.owner, a.repo, n),
		})
	}
	return prs, nil
}

// ListBlockers enumerates the issues blocking the given one via Forgejo's
// dependencies endpoint (the issues this one depends on). linkType is ignored —
// Forgejo has a single dependency relation. State is normalized to "done" when
// the blocker is closed; for an open blocker, Merged reports whether the blocker
// is a merged PR or has a merged PR cross-referenced from it (best-effort).
// Implements source.BlockerLister.
func (a *Adapter) ListBlockers(ctx context.Context, cellID, _ string) ([]source.BlockerRef, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/dependencies", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("codeberg: listing blockers of issue %s: %w", cellID, err)
	}
	var blocking []issue
	if err := json.Unmarshal(body, &blocking); err != nil {
		return nil, fmt.Errorf("codeberg: decoding blockers of issue %s: %w", cellID, err)
	}

	blockers := make([]source.BlockerRef, 0, len(blocking))
	for _, b := range blocking {
		state := b.State
		if state == "closed" {
			state = "done"
		}
		ref := source.BlockerRef{
			ID:     fmt.Sprintf("%d", b.Number),
			Number: fmt.Sprintf("#%d", b.Number),
			Title:  b.Title,
			State:  state,
		}
		switch {
		case b.PullRequest != nil:
			ref.Merged = b.PullRequest.Merged
		case state != "done":
			ref.Merged = a.blockerHasMergedPR(ctx, b.Number)
		}
		blockers = append(blockers, ref)
	}
	return blockers, nil
}

// blockerHasMergedPR reports whether any PR referenced from the blocker issue is
// merged. Best-effort: any lookup failure returns false so a transient API error
// cannot fail the dependency wait.
func (a *Adapter) blockerHasMergedPR(ctx context.Context, issueNumber int) bool {
	prs, err := a.ListPullRequests(ctx, fmt.Sprintf("%d", issueNumber))
	if err != nil {
		aplog.Debug("codeberg: listing PRs of blocker #%d: %v", issueNumber, err)
		return false
	}
	for _, ref := range prs {
		body, err := a.client.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, ref.Number))
		if err != nil {
			aplog.Debug("codeberg: fetching PR #%d of blocker #%d: %v", ref.Number, issueNumber, err)
			continue
		}
		var pr struct {
			Merged bool `json:"merged"`
		}
		if err := json.Unmarshal(body, &pr); err != nil {
			continue
		}
		if pr.Merged {
			return true
		}
	}
	return false
}

// findPRNumber returns the most recent pull request referencing the issue, or 0
// when none is found. It scans the issue timeline (best-effort — Forgejo's
// timeline schema is not guaranteed stable) and keeps the last match so a
// reopened/replacement PR wins over an earlier closed one.
func (a *Adapter) findPRNumber(ctx context.Context, cellID string) (int, error) {
	timeline, err := a.fetchTimeline(ctx, cellID)
	if err != nil {
		return 0, err
	}
	var prNumber int
	for _, e := range timeline {
		if e.RefIssue != nil && e.RefIssue.PullRequest != nil && e.RefIssue.Number != 0 {
			prNumber = e.RefIssue.Number
		}
	}
	return prNumber, nil
}

func (a *Adapter) fetchTimeline(ctx context.Context, cellID string) ([]timelineComment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/timeline", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("codeberg: fetching timeline for issue %s: %w", cellID, err)
	}
	var timeline []timelineComment
	if err := json.Unmarshal(body, &timeline); err != nil {
		return nil, fmt.Errorf("codeberg: decoding timeline for issue %s: %w", cellID, err)
	}
	return timeline, nil
}

// resolveLabelIDs maps label names to repo label ids. When create is true,
// missing labels are created; when false, missing labels are skipped (used by
// RemoveLabels, which has nothing to delete for a label that never existed).
func (a *Adapter) resolveLabelIDs(ctx context.Context, names []string, create bool) ([]int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureLabelsLoadedLocked(ctx); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if id, ok := a.labelByName[key]; ok {
			ids = append(ids, id)
			continue
		}
		if !create {
			continue
		}
		id, err := a.createLabelLocked(ctx, name)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ensureLabelsLoadedLocked populates the label cache once. Caller holds a.mu.
func (a *Adapter) ensureLabelsLoadedLocked(ctx context.Context) error {
	if a.labelsLoaded {
		return nil
	}
	body, err := a.client.get(ctx, fmt.Sprintf("/repos/%s/%s/labels?limit=%d", a.owner, a.repo, perPage))
	if err != nil {
		return fmt.Errorf("codeberg: listing labels: %w", err)
	}
	var labels []label
	if err := json.Unmarshal(body, &labels); err != nil {
		return fmt.Errorf("codeberg: decoding labels: %w", err)
	}
	for _, l := range labels {
		a.labelByName[strings.ToLower(l.Name)] = l.ID
	}
	a.labelsLoaded = true
	return nil
}

// createLabelLocked creates a repo label (Forgejo requires a color) and caches
// its id. Caller holds a.mu. A concurrent/duplicate create (409/422) falls back
// to a cache refresh so the existing label's id is returned.
func (a *Adapter) createLabelLocked(ctx context.Context, name string) (int64, error) {
	body, err := a.client.post(ctx, fmt.Sprintf("/repos/%s/%s/labels", a.owner, a.repo),
		createLabelRequest{Name: name, Color: defaultLabelColor})
	if err != nil {
		if strings.Contains(err.Error(), "status 409") || strings.Contains(err.Error(), "status 422") {
			a.labelsLoaded = false
			if rerr := a.ensureLabelsLoadedLocked(ctx); rerr != nil {
				return 0, rerr
			}
			if id, ok := a.labelByName[strings.ToLower(name)]; ok {
				return id, nil
			}
		}
		return 0, fmt.Errorf("codeberg: creating label %q: %w", name, err)
	}
	var created label
	if err := json.Unmarshal(body, &created); err != nil {
		return 0, fmt.Errorf("codeberg: decoding created label %q: %w", name, err)
	}
	a.labelByName[strings.ToLower(created.Name)] = created.ID
	return created.ID, nil
}

const defaultLabelColor = "#ededed"

func (a *Adapter) toSourceItem(item issue) model.SourceItem {
	labels := make([]string, 0, len(item.Labels))
	for _, l := range item.Labels {
		labels = append(labels, strings.ToLower(l.Name))
	}
	createdAt, _ := time.Parse(time.RFC3339, item.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)

	return model.SourceItem{
		ID:          fmt.Sprintf("%d", item.Number),
		SourceID:    a.ID(),
		Number:      fmt.Sprintf("#%d", item.Number),
		Title:       item.Title,
		Description: item.Body,
		Labels:      labels,
		Type:        "issue",
		State:       item.State,
		URL:         item.HTMLURL,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

// prWebURL prefers the PR's own html_url and falls back to constructing the
// Forgejo PR web path (/{owner}/{repo}/pulls/{n}) from the host.
func prWebURL(pr pullRequest, webBaseURL, owner, repo string) string {
	if pr.HTMLURL != "" {
		return pr.HTMLURL
	}
	return fmt.Sprintf("%s/%s/%s/pulls/%d", webBaseURL, owner, repo, pr.Number)
}

// normalizeStatus maps Forgejo commit-status states to Apiary's check vocabulary.
// Forgejo's set is wider than GitHub's: "warning" is a non-blocking pass and
// "skipped" is reported as skipped.
func normalizeStatus(s string) string {
	switch s {
	case "success", "warning":
		return "passed"
	case "failure", "error":
		return "failed"
	case "pending":
		return "pending"
	case "skipped":
		return "skipped"
	default:
		return "unknown"
	}
}

// isAuthError reports whether a client error is an authorization failure (401/403)
// — a permanent permission problem, not a transient blip to retry past.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403")
}

func formatComment(result model.RunResult) string {
	var b strings.Builder
	if result.Success {
		b.WriteString("✓ **Apiary run complete**")
	} else {
		b.WriteString("✗ **Apiary run failed**")
	}
	fmt.Fprintf(&b, " · worker: `%s`", result.WorkerID)
	fmt.Fprintf(&b, " · duration: %s", result.Duration.Round(time.Second))

	if result.Output != "" {
		b.WriteString("\n\n```\n")
		b.WriteString(result.Output)
		b.WriteString("\n```")
	}
	if result.Error != nil {
		fmt.Fprintf(&b, "\n\n**Error:** `%s`", result.Error.Error())
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
