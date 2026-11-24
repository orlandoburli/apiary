package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
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
	_ source.PRCIStatusPoller = (*Adapter)(nil)
	_ source.SubIssueCreator  = (*Adapter)(nil)
	_ source.BlockerLister    = (*Adapter)(nil)
	_ source.PREventPoller    = (*Adapter)(nil)
)

// ciRepoPolicy governs how this source polls CI for a pull request living in a
// repository other than its own (config.ci_repos). The source's own repository
// is never described by a policy: its PRs are always polled and always require
// checks, so a permanently check-less PR cannot slip through as "no CI".
type ciRepoPolicy struct {
	owner string
	repo  string
	// requireChecks keeps a PR with no check signal at all pending, exactly like
	// the source's own repository. False means the repository is taken not to
	// report CI for PRs, so a signal-less PR concludes as no_checks once it is
	// older than noChecksAfter.
	requireChecks bool
	// noChecksAfter is the grace period, counted from the PR's created_at, before
	// a signal-less PR is concluded as no_checks. It exists to tell "this repo
	// never runs CI" apart from "CI has not registered a check yet".
	noChecksAfter time.Duration
}

// defaultNoChecksAfter is how long a signal-less PR in a require_checks: false
// repository keeps waiting before the wait concludes no_checks.
const defaultNoChecksAfter = 10 * time.Minute

type Adapter struct {
	id         string
	client     *client
	owner      string
	repo       string
	webBaseURL string

	// ciRepos lists the foreign repositories whose PRs this source may poll for
	// CI (config.ci_repos). Empty means only its own repository is pollable.
	ciRepos []ciRepoPolicy

	filterStates []string
	filterLabels []string

	// self is the login of the adapter's own token identity, resolved lazily by
	// selfLogin for PR event author exclusion.
	selfOnce sync.Once
	self     string
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetID(id string) { a.id = id }

func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	repoStr, _ := cfg["repo"].(string)
	if repoStr == "" {
		return fmt.Errorf("github: config.repo is required (e.g. \"owner/repo\")")
	}

	owner, repo, ok := splitRepo(repoStr)
	if !ok {
		return fmt.Errorf("github: config.repo %q must be in \"owner/repo\" format", repoStr)
	}

	ciRepos, err := parseCIRepos(cfg["ci_repos"], owner, repo)
	if err != nil {
		return err
	}

	apiKey, _ := cfg["api_key"].(string)
	baseURL, _ := cfg["base_url"].(string)

	a.owner = owner
	a.repo = repo
	a.ciRepos = ciRepos
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

	aplog.Info("github: configured  repo=%s/%s  host=%s  ci_repos=%d", a.owner, a.repo, a.webBaseURL, len(a.ciRepos))
	return nil
}

// splitRepo parses an "owner/repo" string, rejecting anything that does not name
// both halves.
func splitRepo(s string) (owner, repo string, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// parseCIRepos reads config.ci_repos — the foreign repositories whose PRs this
// source may poll for CI, each with its own waiting policy. A malformed entry is
// a configuration error, not something to guess around: silently dropping it
// would leave the wait pending until max_duration for a repo the operator
// believed was allowed. Naming the source's own repository is rejected too — it
// always requires checks, so the entry can only be a mistake.
func parseCIRepos(raw any, srcOwner, srcRepo string) ([]ciRepoPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("github: config.ci_repos must be a list of {repo, require_checks, no_checks_after} entries")
	}

	policies := make([]ciRepoPolicy, 0, len(entries))
	for i, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("github: config.ci_repos[%d] must be a mapping with a repo key", i)
		}

		repoStr, _ := fields["repo"].(string)
		owner, repo, ok := splitRepo(repoStr)
		if !ok {
			return nil, fmt.Errorf("github: config.ci_repos[%d].repo %q must be in \"owner/repo\" format", i, repoStr)
		}
		if strings.EqualFold(owner, srcOwner) && strings.EqualFold(repo, srcRepo) {
			return nil, fmt.Errorf("github: config.ci_repos[%d] names %s/%s, which is this source's own repository — its PRs are always polled and always require checks", i, owner, repo)
		}

		policy := ciRepoPolicy{owner: owner, repo: repo, noChecksAfter: defaultNoChecksAfter}
		for key, value := range fields {
			switch key {
			case "repo":
				// already read
			case "require_checks":
				b, ok := value.(bool)
				if !ok {
					return nil, fmt.Errorf("github: config.ci_repos[%d].require_checks %v must be true or false", i, value)
				}
				policy.requireChecks = b
			case "no_checks_after":
				s, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("github: config.ci_repos[%d].no_checks_after %v must be a duration string (e.g. \"10m\")", i, value)
				}
				if strings.TrimSpace(s) == "" {
					continue
				}
				d, err := time.ParseDuration(strings.TrimSpace(s))
				if err != nil {
					return nil, fmt.Errorf("github: config.ci_repos[%d].no_checks_after %q: %w", i, s, err)
				}
				if d <= 0 {
					return nil, fmt.Errorf("github: config.ci_repos[%d].no_checks_after %q must be positive", i, s)
				}
				policy.noChecksAfter = d
			default:
				// A typo (require_check) would otherwise silently do nothing.
				return nil, fmt.Errorf("github: config.ci_repos[%d] has unknown key %q (accepted: repo, require_checks, no_checks_after)", i, key)
			}
		}
		policies = append(policies, policy)
	}
	return policies, nil
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

// CreateSubIssue creates a new issue from the child item (title, body, labels)
// and links it under the parent issue via GitHub's sub-issues API. It implements
// source.SubIssueCreator, materializing a spawned InternalTask as a real issue so
// the normal poll→route loop picks it up by its labels.
//
// The link step is best-effort: if the issue is created but linking fails (e.g.
// the token lacks the needed scope, or sub-issues are unavailable on the host),
// the created issue is still returned with the failure logged. Returning the item
// lets the caller persist the child's binding so a re-run never creates a second
// copy — the parent-child link is navigational, while routing is driven by labels.
func (a *Adapter) CreateSubIssue(ctx context.Context, parent, child model.SourceItem) (model.SourceItem, error) {
	// Labels must exist before they can be applied; ensureLabel is a no-op when the
	// label already exists (GitHub returns 422, which it tolerates).
	for _, name := range child.Labels {
		if err := a.ensureLabel(ctx, name); err != nil {
			return model.SourceItem{}, err
		}
	}

	createPath := fmt.Sprintf("/repos/%s/%s/issues", a.owner, a.repo)
	body, err := a.client.post(ctx, createPath, createIssueRequest{
		Title:  child.Title,
		Body:   child.Description,
		Labels: child.Labels,
	})
	if err != nil {
		return model.SourceItem{}, fmt.Errorf("github: creating sub-issue under %s: %w", parent.ID, err)
	}
	var created issue
	if err := json.Unmarshal(body, &created); err != nil {
		return model.SourceItem{}, fmt.Errorf("github: decoding created sub-issue: %w", err)
	}

	// Link the new issue under the parent. parent.ID is the parent's issue number.
	linkPath := fmt.Sprintf("/repos/%s/%s/issues/%s/sub_issues", a.owner, a.repo, parent.ID)
	if _, err := a.client.post(ctx, linkPath, subIssueRequest{SubIssueID: created.ID}); err != nil {
		aplog.Error("github: created sub-issue #%d but could not link it under #%s: %v", created.Number, parent.ID, err)
	}

	return a.toSourceItem(created), nil
}

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

// AddLabels appends the named labels to an issue via the additive
// POST /issues/{n}/labels endpoint, leaving every other label untouched.
// It must NOT send the full label set (PATCH with labels replaces it): the
// dispatched cell's label snapshot goes stale while a run executes, and an
// agent may legitimately swap labels mid-run — replaying the snapshot here
// would silently revert that. Implements source.LabelAdder.
func (a *Adapter) AddLabels(ctx context.Context, cell model.SourceItem, names []string) error {
	if len(names) == 0 {
		return nil
	}

	labelList := make([]string, 0, len(names))
	for _, n := range names {
		if err := a.ensureLabel(ctx, n); err != nil {
			return err
		}
		labelList = append(labelList, n)
	}

	issueNo := cell.ID
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/labels", a.owner, a.repo, issueNo)
	_, err := a.client.post(ctx, path, labelListRequest{Labels: labelList})
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

		// Collect every cross-referenced PR, most recent first. Any PR that merely
		// MENTIONS the issue shows up here (including closed PRs from sibling
		// issues and open PRs that only cite it), so we can't just take the first
		// hit: an issue's real PR must be picked among the candidates. An OPEN PR
		// whose head branch carries the issue number (e.g. agent/task-1958) wins;
		// otherwise the most recently referenced OPEN PR; only when no candidate
		// is open do we fall back to the most recent one regardless of state
		// (e.g. polling right after a merge).
		var candidates []int
		seen := map[int]bool{}
		for i := len(timeline) - 1; i >= 0; i-- {
			event := timeline[i]
			if event.Event == "cross-referenced" && event.Source.Type == "issue" && event.Source.Issue.PullRequest.URL != "" {
				if n := event.Source.Issue.Number; !seen[n] {
					seen[n] = true
					candidates = append(candidates, n)
				}
			}
		}

		if len(candidates) == 0 {
			return source.CIStatus{Status: "pending"}, nil // No PR found yet; still pending
		}

		// Fetch candidate PRs until an open one is found. A fetch failure is NOT
		// "pending": we found a real PR but can't read it. Surface it as an error
		// so the caller logs it instead of masking a permanent problem (e.g. a
		// token lacking Pull requests: Read, which returns 403) as an endless wait.
		var fallbackBody, openBody []byte
		prBody = nil
		for _, prNumber := range candidates {
			prPath = fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, prNumber)
			body, err := a.client.get(ctx, prPath)
			if err != nil {
				if isAuthError(err) {
					return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: cannot read PR #%d for issue %s — the configured token likely lacks 'Pull requests: Read' (and 'Contents: Read'): %w", prNumber, cellID, err)
				}
				return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: fetching PR #%d for issue %s: %w", prNumber, cellID, err)
			}
			var candidate pullRequest
			if err := json.Unmarshal(body, &candidate); err != nil {
				continue
			}
			if candidate.State == "open" {
				if branchReferencesIssue(candidate.Head.Ref, cellID) {
					prBody = body
					break
				}
				if openBody == nil {
					openBody = body
				}
			}
			if fallbackBody == nil {
				fallbackBody = body
			}
		}
		if prBody == nil {
			prBody = openBody
		}
		if prBody == nil {
			prBody = fallbackBody
		}
		if prBody == nil {
			return source.CIStatus{Status: "pending"}, nil
		}
	}

	return a.ciStatusFromPRBody(ctx, prBody, cellID, a.owner, a.repo, nil)
}

// branchReferencesIssue reports whether a PR head branch names the issue number
// as a standalone token (agent/task-1958, 1958-fix, issue_1958), so the issue's
// own PR can be told apart from other open PRs that merely cite it. Digits must
// not continue on either side: task-19580 does not reference 1958.
func branchReferencesIssue(ref, issue string) bool {
	if ref == "" || issue == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(ref[from:], issue)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(issue)
		before := start == 0 || !isDigit(ref[start-1])
		after := end == len(ref) || !isDigit(ref[end])
		if before && after {
			return true
		}
		from = start + 1
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// ciStatusFromPRBody synthesizes a CI status from an already-fetched pull
// request payload. It is the half of the CI check that is the same whether the
// PR was resolved from an issue (PollCIStatus) or addressed directly by number
// (PollCIStatusForPR); ref only labels log lines and error messages.
//
// owner/repo are the repository the PR actually lives in — the source's own for
// PollCIStatus and for a same-repo ref, otherwise the ci_repos entry that
// allowed it — so the commit-status and check-run calls follow a foreign PR to
// its own repository. policy is nil for the source's own repository, which
// always requires checks.
func (a *Adapter) ciStatusFromPRBody(ctx context.Context, prBody []byte, ref, owner, repo string, policy *ciRepoPolicy) (source.CIStatus, error) {
	var pr pullRequest
	if err := json.Unmarshal(prBody, &pr); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: decoding PR %s: %w", ref, err)
	}

	// A PR with merge conflicts can never merge until someone rebases/resolves it,
	// so waiting for CI on it is pointless. Surface the conflict immediately as a
	// distinct terminal status and skip the CI aggregation entirely — this lets the
	// wait_for step abort and hand the conflict back to the engineer agent rather
	// than burning the whole timeout. mergeable_state == "dirty" is GitHub's
	// definitive "has conflicts" signal; "unknown"/null means GitHub is still
	// computing mergeability (lazy, async) so we fall through and keep waiting.
	if pr.MergeableState == "dirty" {
		aplog.Info("github: PR #%d for %s has merge conflicts (mergeable_state=dirty)", pr.Number, ref)
		return source.CIStatus{Status: "conflict", URL: pr.HTMLURL}, nil
	}

	headSHA := pr.Head.SHA
	if headSHA == "" {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: PR %s has no head SHA", ref)
	}

	// Get the combined status for this commit.
	statusPath := fmt.Sprintf("/repos/%s/%s/commits/%s/status", owner, repo, headSHA)
	statusBody, err := a.client.get(ctx, statusPath)
	if err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: fetching status for %s: %w", headSHA, err)
	}

	var commitStatus commitStatus
	if err := json.Unmarshal(statusBody, &commitStatus); err != nil {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: decoding commit status: %w", err)
	}

	// Get check runs for more detailed status information.
	checksPath := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, headSHA)
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
	//   - else (no signals at all)   → pending (CI hasn't started) — or
	//     no_checks for a ci_repos entry that does not require checks whose PR
	//     is past its grace period (that repository reports no CI for PRs)
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
		// No checks and no statuses. Normally that means CI has not started yet.
		// For a ci_repos entry that does not require checks, the PR's age decides:
		// past the grace period the repository is taken to report no CI at all for
		// its PRs, so the wait concludes no_checks instead of burning max_duration.
		if policy != nil && !policy.requireChecks && pastGrace(pr.CreatedAt, policy.noChecksAfter) {
			aplog.Info("github: PR #%d for %s in %s/%s has no CI signal after its %v grace period — reporting no_checks",
				pr.Number, ref, owner, repo, policy.noChecksAfter)
			return source.CIStatus{Status: "no_checks", URL: pr.HTMLURL, Checks: checks}, nil
		}
		overall = "pending" // no checks and no statuses yet — CI not started
	}

	return source.CIStatus{Status: overall, URL: pr.HTMLURL, Checks: checks}, nil
}

// pastGrace reports whether a PR created at createdAt (RFC3339, as returned by
// GitHub) is at least grace old. A missing or unparseable created_at yields
// false: without an age there is no evidence the repository skips CI, so the
// wait keeps going rather than concluding no_checks on a guess.
func pastGrace(createdAt string, grace time.Duration) bool {
	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil || created.IsZero() {
		return false
	}
	return time.Since(created) >= grace
}

// PollCIStatusForPR checks the CI status of one pull request, for tasks whose
// own source cannot resolve a PR (a Jira-sourced task whose agents push to
// GitHub). Implements source.PRCIStatusPoller.
//
// The PR may live in this source's own repository or in one declared under
// config.ci_repos, which is what lets a pipeline hosted elsewhere poll a
// repository that runs no CI at all (#8151). A ref whose URL points at a
// repository neither of those covers is a configuration error, not a transient
// one: reporting some same-numbered PR of another repo would be worse than
// failing. A ref with no URL is trusted as-is — the number is all the caller had.
func (a *Adapter) PollCIStatusForPR(ctx context.Context, pr source.PullRequestRef) (source.CIStatus, error) {
	if pr.Number <= 0 {
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: pull request ref has no number")
	}
	owner, repo, policy, err := a.resolvePRRepo(pr)
	if err != nil {
		return source.CIStatus{Status: "unknown"}, err
	}

	prPath := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, pr.Number)
	body, err := a.client.get(ctx, prPath)
	if err != nil {
		if isAuthError(err) {
			return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: cannot read PR #%d — the configured token likely lacks 'Pull requests: Read' (and 'Contents: Read'): %w", pr.Number, err)
		}
		return source.CIStatus{Status: "unknown"}, fmt.Errorf("github: fetching PR #%d: %w", pr.Number, err)
	}
	return a.ciStatusFromPRBody(ctx, body, fmt.Sprintf("#%d", pr.Number), owner, repo, policy)
}

// resolvePRRepo maps a PR ref to the repository it lives in and the policy that
// governs waiting on it:
//
//   - no URL, or a URL naming no owner/repo → this source's repository, which
//     always requires checks (the number is all the caller had);
//   - a URL served by a different host → ErrUnsupported (not a PR of this forge);
//   - this source's own owner/repo → its repository, always requiring checks;
//   - an entry of config.ci_repos → that entry's policy;
//   - anything else → ErrUnsupported, naming the allowed ci_repos.
//
// Both URL shapes are accepted: https://github.com/o/r/pull/N and
// https://api.github.com/repos/o/r/pulls/N.
func (a *Adapter) resolvePRRepo(pr source.PullRequestRef) (owner, repo string, policy *ciRepoPolicy, err error) {
	if strings.TrimSpace(pr.URL) == "" {
		return a.owner, a.repo, nil, nil
	}
	u, perr := url.Parse(pr.URL)
	if perr != nil {
		return a.owner, a.repo, nil, nil
	}
	if !a.servesHost(u.Host) {
		return "", "", nil, fmt.Errorf("github: pull request %s is served by %s, not by this source's host %s: %w",
			pr.URL, u.Host, a.webBaseURL, source.ErrUnsupported)
	}

	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	// An API URL (/repos/<owner>/<repo>/pulls/N) carries the pair one level in.
	if len(segs) >= 3 && segs[0] == "repos" {
		segs = segs[1:]
	}
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return a.owner, a.repo, nil, nil
	}

	if strings.EqualFold(segs[0], a.owner) && strings.EqualFold(segs[1], a.repo) {
		return a.owner, a.repo, nil, nil
	}
	for i := range a.ciRepos {
		if strings.EqualFold(segs[0], a.ciRepos[i].owner) && strings.EqualFold(segs[1], a.ciRepos[i].repo) {
			return a.ciRepos[i].owner, a.ciRepos[i].repo, &a.ciRepos[i], nil
		}
	}

	return "", "", nil, fmt.Errorf("github: pull request %s belongs to %s/%s, but source %q is configured for %s/%s and its ci_repos allow %s: %w",
		pr.URL, segs[0], segs[1], a.id, a.owner, a.repo, a.allowedCIRepos(), source.ErrUnsupported)
}

// servesHost reports whether host is one this adapter's client can reach — the
// web host of PR URLs and the API host (they differ for github.com, where a
// linked PR may arrive in either form). An empty host, or an adapter connected
// without a web host to compare against, is tolerated: only a host that clearly
// belongs to another forge is refused.
func (a *Adapter) servesHost(host string) bool {
	if host == "" || a.webBaseURL == "" {
		return true
	}
	for _, base := range []string{a.webBaseURL, a.apiBaseURL()} {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			continue
		}
		if strings.EqualFold(u.Host, host) {
			return true
		}
	}
	return false
}

// apiBaseURL is the API base the client calls, or "" when no client is wired.
func (a *Adapter) apiBaseURL() string {
	if a.client == nil {
		return ""
	}
	return a.client.baseURL
}

// allowedCIRepos renders the configured ci_repos for an error message.
func (a *Adapter) allowedCIRepos() string {
	if len(a.ciRepos) == 0 {
		return "none (no ci_repos configured)"
	}
	names := make([]string, 0, len(a.ciRepos))
	for _, p := range a.ciRepos {
		names = append(names, p.owner+"/"+p.repo)
	}
	return strings.Join(names, ", ")
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

// ListBlockers enumerates the issues blocking the given one via GitHub's issue
// dependencies API (GET .../issues/{n}/dependencies/blocked_by). linkType is
// ignored — GitHub has a single "blocked by" relation. State is normalized to
// "done" when the blocker is closed, otherwise its raw state. For an open
// blocker, Merged reports whether any pull request cross-referenced from it is
// merged (best-effort: a PR lookup failure leaves Merged false rather than
// failing the whole check). Implements source.BlockerLister.
func (a *Adapter) ListBlockers(ctx context.Context, cellID, _ string) ([]source.BlockerRef, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%s/dependencies/blocked_by", a.owner, a.repo, cellID)
	body, err := a.client.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("github: listing blockers of issue %s: %w", cellID, err)
	}

	var blocking []issue
	if err := json.Unmarshal(body, &blocking); err != nil {
		return nil, fmt.Errorf("github: decoding blockers of issue %s: %w", cellID, err)
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
		if state != "done" {
			ref.Merged = a.blockerHasMergedPR(ctx, b.Number)
		}
		blockers = append(blockers, ref)
	}
	return blockers, nil
}

// blockerHasMergedPR reports whether any pull request cross-referenced from the
// blocker issue is merged. Best-effort: any lookup failure returns false (the
// dependency wait just keeps polling), so a transient API error cannot fail the
// workflow step.
func (a *Adapter) blockerHasMergedPR(ctx context.Context, issueNumber int) bool {
	prs, err := a.ListPullRequests(ctx, fmt.Sprintf("%d", issueNumber))
	if err != nil {
		aplog.Debug("github: listing PRs of blocker #%d: %v", issueNumber, err)
		return false
	}
	for _, ref := range prs {
		prPath := fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, ref.Number)
		body, err := a.client.get(ctx, prPath)
		if err != nil {
			aplog.Debug("github: fetching PR #%d of blocker #%d: %v", ref.Number, issueNumber, err)
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
