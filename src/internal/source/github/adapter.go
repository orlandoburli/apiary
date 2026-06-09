package github

import (
	"context"
	"encoding/json"
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

// Compile-time checks: the GitHub adapter supports the optional source
// capabilities used by the dispatcher and the workflow engine.
var (
	_ source.StateSetter      = (*Adapter)(nil)
	_ source.LabelAdder       = (*Adapter)(nil)
	_ source.LabelRemover     = (*Adapter)(nil)
	_ source.TaskPoller       = (*Adapter)(nil)
	_ source.CIStatusPoller   = (*Adapter)(nil)
)

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

func (a *Adapter) Poll(ctx context.Context, _ time.Time) ([]model.SourceItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", a.owner, a.repo)

	state := "open"
	if len(a.filterStates) > 0 {
		state = a.filterStates[0]
	}
	params := url.Values{
		"state":     {state},
		"sort":      {"created"},
		"direction": {"asc"},
	}

	issues, err := a.client.getAllIssues(ctx, path, params)
	if err != nil {
		return nil, fmt.Errorf("github: polling issues: %w", err)
	}

	var cells []model.SourceItem
	for _, item := range issues {
		// GitHub's /issues endpoint also returns pull requests (every PR is an
		// issue in the API). PRs are implementation artifacts, not work items,
		// so we never ingest them as tasks.
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
	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/labels", a.owner, a.repo, issueNo)
	_, err := a.client.post(ctx, path, labelListRequest{Labels: []string{"in-progress"}})
	if err != nil {
		return fmt.Errorf("github: acknowledging %s: %w", cell.ID, err)
	}
	return nil
}

func (a *Adapter) WriteResult(ctx context.Context, cell model.SourceItem, result model.RunResult) error {
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

// PollTask fetches the current state of a single issue plus its comments, for
// the workflow engine to evaluate approval-step conditions. Implements
// source.TaskPoller.
func (a *Adapter) PollTask(ctx context.Context, cellID string) (model.SourceItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return model.SourceItem{}, fmt.Errorf("github: poll task %s: %w", cellID, err)
	}
	var item issue
	if err := json.Unmarshal(body, &item); err != nil {
		return model.SourceItem{}, fmt.Errorf("github: decoding issue %s: %w", cellID, err)
	}

	cell := a.toSourceItem(item)
	comments, err := a.fetchComments(ctx, cellID)
	if err != nil {
		aplog.Debug("github: fetch comments for %s: %v", cellID, err)
	} else {
		cell.Comments = comments
	}
	return cell, nil
}

// fetchComments retrieves an issue's comments as model.Comment values.
func (a *Adapter) fetchComments(ctx context.Context, issueNo string) ([]model.Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/comments", a.owner, a.repo, issueNo)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var raw []comment
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decoding comments for %s: %w", issueNo, err)
	}
	out := make([]model.Comment, 0, len(raw))
	for _, c := range raw {
		created, _ := time.Parse(time.RFC3339, c.CreatedAt)
		out = append(out, model.Comment{ID: fmt.Sprintf("%d", c.ID), Body: c.Body, CreatedAt: created})
	}
	return out, nil
}

func (a *Adapter) SetState(ctx context.Context, cell model.SourceItem, stateName string) error {
	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s", a.owner, a.repo, issueNo)
	_, err := a.client.patch(ctx, path, issueRequest{State: stateName})
	if err != nil {
		return fmt.Errorf("github: setting state %q on %s: %w", stateName, cell.ID, err)
	}
	return nil
}

func (a *Adapter) AddLabels(ctx context.Context, cell model.SourceItem, names []string) error {
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

// RemoveLabels deletes the named labels from an issue, one DELETE per label so
// the remaining labels are untouched. A label that is already absent (GitHub
// returns 404) is ignored. Implements source.LabelRemover.
func (a *Adapter) RemoveLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	issueNo := cell.ID
	for _, name := range names {
		path := fmt.Sprintf("/repos/%s/%s/issues/%s/labels/%s",
			a.owner, a.repo, issueNo, url.PathEscape(name))
		if _, err := a.client.delete(ctx, path); err != nil {
			if strings.Contains(err.Error(), "status 404") {
				continue // label already absent — nothing to remove
			}
			return fmt.Errorf("github: removing label %q from %s: %w", name, cell.ID, err)
		}
	}
	return nil
}

// PollCIStatus fetches the CI status of a PR/issue from GitHub. It queries the
// combined status and check runs for the PR and synthesizes an overall status.
// Implements source.CIStatusPoller.
func (a *Adapter) PollCIStatus(ctx context.Context, cellID string) (source.CIStatus, error) {
	// cellID is the issue number. Find the associated PR.
	// First try direct lookup (in case issue number == PR number).
	prPath := fmt.Sprintf("/repos/%s/%s/pulls/%s", a.owner, a.repo, cellID)
	prBody, err := a.client.get(ctx, prPath)

	// If direct lookup fails, find PR via issue timeline cross-reference.
	if err != nil {
		timelinePath := fmt.Sprintf("/repos/%s/%s/issues/%s/timeline", a.owner, a.repo, cellID)
		timelineBody, timelineErr := a.client.get(ctx, timelinePath)
		if timelineErr != nil {
			return source.CIStatus{Status: "pending"}, nil // No timeline; still pending
		}

		var timeline []struct {
			Event  string `json:"event"`
			Source struct {
				Type  string `json:"type"`
				Issue struct {
					Number      int `json:"number"`
					PullRequest struct {
						URL string `json:"url"`
					} `json:"pull_request"`
				} `json:"issue"`
			} `json:"source"`
		}
		if err := json.Unmarshal(timelineBody, &timeline); err != nil {
			return source.CIStatus{Status: "pending"}, nil
		}

		// Find the cross-reference event pointing to a PR
		var prNumber int
		for _, event := range timeline {
			if event.Event == "cross-referenced" && event.Source.Type == "issue" && event.Source.Issue.PullRequest.URL != "" {
				prNumber = event.Source.Issue.Number
				break
			}
		}

		if prNumber == 0 {
			return source.CIStatus{Status: "pending"}, nil // No PR found yet; still pending
		}

		// Fetch the PR details. A failure here is NOT "pending": we found a real PR
		// but can't read it. Surface it as an error so the caller logs it instead of
		// masking a permanent problem (e.g. a token lacking Pull requests: Read,
		// which returns 403) as an endless wait.
		prPath = fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, prNumber)
		prBody, err = a.client.get(ctx, prPath)
		if err != nil {
			if isAuthError(err) {
				return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: cannot read PR #%d for issue %s — the configured token likely lacks 'Pull requests: Read' (and 'Contents: Read'): %w", prNumber, cellID, err)
			}
			return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: fetching PR #%d for issue %s: %w", prNumber, cellID, err)
		}
	}

	var pr pullRequest
	if err := json.Unmarshal(prBody, &pr); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: decoding PR %s: %w", cellID, err)
	}

	headSHA := pr.Head.SHA
	if headSHA == "" {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: PR %s has no head SHA", cellID)
	}

	// Get the combined status for this commit.
	statusPath := fmt.Sprintf("/repos/%s/%s/commits/%s/status", a.owner, a.repo, headSHA)
	statusBody, err := a.client.get(ctx, statusPath)
	if err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: fetching status for %s: %w", headSHA, err)
	}

	var commitStatus commitStatus
	if err := json.Unmarshal(statusBody, &commitStatus); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: decoding commit status: %w", err)
	}

	// Get check runs for more detailed status information.
	checksPath := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", a.owner, a.repo, headSHA)
	checksBody, err := a.client.get(ctx, checksPath)
	if err != nil {
		// Check runs might not be available; fall back to status.
		checksBody = []byte("{}")
	}

	var checkRuns checkRunsResponse
	_ = json.Unmarshal(checksBody, &checkRuns)

	// Synthesize the overall status from BOTH check runs (GitHub Actions) and the
	// legacy combined commit status. A repo that runs CI purely via Actions has an
	// EMPTY combined status whose state defaults to "pending" (total_count == 0) —
	// trusting that alone would report pending forever even when every check is
	// green. So we aggregate every signal:
	//   - any failed signal      → failed
	//   - else any still-running → pending
	//   - else (≥1 signal, all good) → passed
	//   - else (no signals at all)   → pending (CI hasn't started)
	anyFail, anyPending, anySignal := false, false, false

	checks := make([]struct {
		Name   string
		Status string
	}, 0)

	for _, run := range checkRuns.CheckRuns {
		anySignal = true
		var norm string
		if run.Status != "completed" {
			norm = "pending" // queued / in_progress
			anyPending = true
		} else {
			norm = normalizeGitHubStatus(run.Conclusion) // success/skipped/neutral → passed/skipped; failure/… → failed
			if norm == "failed" {
				anyFail = true
			}
		}
		checks = append(checks, struct {
			Name   string
			Status string
		}{Name: run.Name, Status: norm})
	}

	// Legacy commit statuses count only when they actually exist.
	if commitStatus.TotalCount > 0 {
		for _, st := range commitStatus.Statuses {
			anySignal = true
			norm := normalizeGitHubStatus(st.State)
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
	}

	overall := "pending"
	switch {
	case anyFail:
		overall = "failed"
	case anyPending:
		overall = "pending"
	case anySignal:
		overall = "passed"
	default:
		overall = "pending" // no checks and no statuses yet — CI not started
	}

	return source.CIStatus{Status: overall, URL: pr.HTMLURL, Checks: checks}, nil
}

// ListPullRequests returns every pull request cross-referenced from the issue,
// oldest first. It makes a single API call (the issue timeline) and derives each
// PR's html_url from owner/repo/number, so the cost is constant regardless of how
// many PRs are linked — the dashboard only needs a URL to open in the browser.
// Implements source.PullRequestLister.
func (a *Adapter) ListPullRequests(ctx context.Context, cellID string) ([]source.PullRequestRef, error) {
	timelinePath := fmt.Sprintf("/repos/%s/%s/issues/%s/timeline", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, timelinePath)
	if err != nil {
		return nil, fmt.Errorf("github: fetching timeline for issue %s: %w", cellID, err)
	}

	var timeline []struct {
		Event  string `json:"event"`
		Source struct {
			Type  string `json:"type"`
			Issue struct {
				Number      int `json:"number"`
				PullRequest struct {
					URL string `json:"url"`
				} `json:"pull_request"`
			} `json:"issue"`
		} `json:"source"`
	}
	if err := json.Unmarshal(body, &timeline); err != nil {
		return nil, fmt.Errorf("github: decoding timeline for issue %s: %w", cellID, err)
	}

	// A PR can be cross-referenced more than once; dedup by number while keeping
	// first-seen (chronological) order so the caller's tail is the most recent PR.
	var prs []source.PullRequestRef
	seen := make(map[int]bool)
	for _, e := range timeline {
		if e.Event != "cross-referenced" || e.Source.Type != "issue" || e.Source.Issue.PullRequest.URL == "" {
			continue
		}
		n := e.Source.Issue.Number
		if n == 0 || seen[n] {
			continue
		}
		seen[n] = true
		prs = append(prs, source.PullRequestRef{
			Number: n,
			URL:    fmt.Sprintf("https://github.com/%s/%s/pull/%d", a.owner, a.repo, n),
		})
	}
	return prs, nil
}

// isAuthError reports whether a client error is a GitHub authorization failure
// (401 Unauthorized or 403 Forbidden) — typically a missing token permission,
// which is permanent until the token is fixed (not a transient blip to retry past).
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403")
}

func normalizeGitHubStatus(s string) string {
	switch s {
	case "success":
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

func (a *Adapter) ensureLabel(ctx context.Context, name string) error {
	path := fmt.Sprintf("/repos/%s/%s/labels", a.owner, a.repo)
	_, err := a.client.post(ctx, path, labelCreateRequest{Name: name})
	if err != nil && !strings.Contains(err.Error(), "status 422") {
		return fmt.Errorf("github: ensuring label %q: %w", name, err)
	}
	return nil
}

func (a *Adapter) toSourceItem(item issue) model.SourceItem {
	labels := make([]string, 0, len(item.Labels))
	for _, l := range item.Labels {
		labels = append(labels, strings.ToLower(l.Name))
	}
	createdAt, _ := time.Parse(time.RFC3339, item.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)

	// Poll skips pull requests, so every ingested item is a plain issue.
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
