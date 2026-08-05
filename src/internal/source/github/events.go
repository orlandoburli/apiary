package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// prPathRe extracts a PR number from a comment html_url: an issue comment on a
// pull request deep-links to .../pull/<n>#issuecomment-<id>, while a comment on
// a plain issue deep-links to .../issues/<n>#... — the path segment is how the
// two are told apart without an extra API call.
var prPathRe = regexp.MustCompile(`/pull/(\d+)(?:[#/]|$)`)

// closingRefRe matches a closing-keyword issue reference in a PR body
// ("Closes #42", "fixes owner/repo#42"), the strongest signal of the PR's
// originating work item.
var closingRefRe = regexp.MustCompile(`(?i)(?:close[sd]?|fix(?:es|ed)?|resolve[sd]?)[\s:]+(?:[\w.-]+/[\w.-]+)?#(\d+)`)

// issueRefRe matches any plain issue reference ("#42") as the fallback signal.
var issueRefRe = regexp.MustCompile(`#(\d+)`)

// PollPREvents returns the pull-request events (conversation comments, inline
// review comments, and review submissions) created since the watermark, oldest
// first per category. Implements source.PREventPoller.
//
// Loop prevention: events authored by the adapter's own token identity (the
// account the daemon comments as) and by Bot-typed users are dropped here, so an
// agent's own PR comments can never re-trigger a workflow.
//
// RelatedItemID is resolved best-effort by scanning the PR body for an issue
// reference (closing keywords first), reusing one PR fetch per distinct PR and
// poll. A PR with no discoverable parent issue yields events with an empty
// RelatedItemID — the dispatcher then binds a standalone task for the PR.
func (a *Adapter) PollPREvents(ctx context.Context, since time.Time) ([]model.SourceEvent, error) {
	self := a.selfLogin(ctx)
	sinceParam := since.UTC().Format(time.RFC3339)
	var events []model.SourceEvent

	// One PR fetch per distinct number per poll: PRURL + related-issue lookup.
	prCache := map[int]*pullRequest{}
	prFor := func(n int) *pullRequest {
		if pr, ok := prCache[n]; ok {
			return pr
		}
		body, err := a.client.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", a.owner, a.repo, n))
		if err != nil {
			aplog.Debug("github: events: fetch PR #%d: %v", n, err)
			prCache[n] = nil
			return nil
		}
		var pr pullRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			prCache[n] = nil
			return nil
		}
		prCache[n] = &pr
		return &pr
	}
	describe := func(n int) (prURL, relatedItemID string) {
		prURL = fmt.Sprintf("%s/%s/%s/pull/%d", a.webBaseURL, a.owner, a.repo, n)
		if pr := prFor(n); pr != nil {
			if pr.HTMLURL != "" {
				prURL = pr.HTMLURL
			}
			relatedItemID = findRelatedIssue(pr.Body, n)
		}
		return prURL, relatedItemID
	}

	// 1. PR conversation comments — the /issues/comments endpoint returns
	// comments on issues AND pull requests; the html_url path picks out the PRs.
	comments, err := decodePages[issueComment](ctx, a.client,
		fmt.Sprintf("/repos/%s/%s/issues/comments", a.owner, a.repo),
		url.Values{"sort": {"updated"}, "direction": {"asc"}, "since": {sinceParam}})
	if err != nil {
		return nil, fmt.Errorf("github: polling issue comments: %w", err)
	}
	for _, cm := range comments {
		n := prNumberFromURL(cm.HTMLURL)
		if n == 0 || a.skipAuthor(cm.User, self) {
			continue
		}
		// `since` filters on updated_at, so an edited old comment reappears here;
		// gating on created_at keeps edits from firing as new events.
		created, _ := time.Parse(time.RFC3339, cm.CreatedAt)
		if !created.After(since) {
			continue
		}
		prURL, itemID := describe(n)
		events = append(events, model.SourceEvent{
			ID:                fmt.Sprintf("comment-%d", cm.ID),
			SourceID:          a.ID(),
			Kind:              model.EventPRComment,
			PRNumber:          n,
			PRURL:             prURL,
			Author:            cm.User.Login,
			AuthorAssociation: cm.AuthorAssociation,
			Body:              cm.Body,
			SubmittedAt:       created,
			RelatedItemID:     itemID,
		})
	}

	// 2. Inline review comments (comments on a diff line).
	rcomments, err := decodePages[reviewComment](ctx, a.client,
		fmt.Sprintf("/repos/%s/%s/pulls/comments", a.owner, a.repo),
		url.Values{"sort": {"updated"}, "direction": {"asc"}, "since": {sinceParam}})
	if err != nil {
		return nil, fmt.Errorf("github: polling review comments: %w", err)
	}
	for _, rc := range rcomments {
		n := prNumberFromAPIURL(rc.PullRequestURL)
		if n == 0 || a.skipAuthor(rc.User, self) {
			continue
		}
		created, _ := time.Parse(time.RFC3339, rc.CreatedAt)
		if !created.After(since) {
			continue
		}
		prURL, itemID := describe(n)
		events = append(events, model.SourceEvent{
			ID:                fmt.Sprintf("review-comment-%d", rc.ID),
			SourceID:          a.ID(),
			Kind:              model.EventPRComment,
			PRNumber:          n,
			PRURL:             prURL,
			Author:            rc.User.Login,
			AuthorAssociation: rc.AuthorAssociation,
			Body:              rc.Body,
			SubmittedAt:       created,
			RelatedItemID:     itemID,
		})
	}

	// 3. Review submissions on recently-updated PRs. Reviews have no since-able
	// list endpoint, so scope the walk to PRs updated inside the window (the
	// updated_desc listing stops at the first stale PR).
	prs, err := decodePages[pullRequest](ctx, a.client,
		fmt.Sprintf("/repos/%s/%s/pulls", a.owner, a.repo),
		url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}})
	if err != nil {
		return nil, fmt.Errorf("github: polling pull requests: %w", err)
	}
	for _, pr := range prs {
		updated, _ := time.Parse(time.RFC3339, pr.UpdatedAt)
		if !updated.After(since) {
			break // updated_desc order: everything after this is older
		}
		pr := pr
		prCache[pr.Number] = &pr
		reviews, err := decodePages[review](ctx, a.client,
			fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", a.owner, a.repo, pr.Number), nil)
		if err != nil {
			aplog.Debug("github: events: list reviews of PR #%d: %v", pr.Number, err)
			continue
		}
		for _, rv := range reviews {
			var kind string
			switch rv.State {
			case "APPROVED":
				kind = model.EventPRReviewApproved
			case "CHANGES_REQUESTED":
				kind = model.EventPRReviewChangesRequest
			default:
				continue
			}
			submitted, _ := time.Parse(time.RFC3339, rv.SubmittedAt)
			if !submitted.After(since) || a.skipAuthor(rv.User, self) {
				continue
			}
			prURL, itemID := describe(pr.Number)
			events = append(events, model.SourceEvent{
				ID:                fmt.Sprintf("review-%d", rv.ID),
				SourceID:          a.ID(),
				Kind:              kind,
				PRNumber:          pr.Number,
				PRURL:             prURL,
				Author:            rv.User.Login,
				AuthorAssociation: rv.AuthorAssociation,
				Body:              rv.Body,
				SubmittedAt:       submitted,
				RelatedItemID:     itemID,
			})
		}
	}

	return events, nil
}

// selfLogin returns the login of the adapter's own token identity, fetched once
// per process (GET /user) and cached. Empty on failure (e.g. an installation
// token with no /user) — bot-type filtering still applies.
func (a *Adapter) selfLogin(ctx context.Context) string {
	a.selfOnce.Do(func() {
		body, err := a.client.get(ctx, "/user")
		if err != nil {
			aplog.Debug("github: events: resolve own login: %v", err)
			return
		}
		var u user
		if err := json.Unmarshal(body, &u); err != nil {
			return
		}
		a.self = u.Login
		aplog.Debug("github: events: own login is %q (excluded from PR events)", u.Login)
	})
	return a.self
}

// skipAuthor reports whether an event author must be dropped: the daemon's own
// token identity, or any bot account.
func (a *Adapter) skipAuthor(u user, self string) bool {
	if u.Type == "Bot" || strings.HasSuffix(strings.ToLower(u.Login), "[bot]") {
		return true
	}
	return self != "" && strings.EqualFold(u.Login, self)
}

// prNumberFromURL extracts the PR number from a browser html_url containing a
// /pull/<n> segment, or 0 when the URL points at a plain issue.
func prNumberFromURL(htmlURL string) int {
	m := prPathRe.FindStringSubmatch(htmlURL)
	if len(m) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// prNumberFromAPIURL extracts the PR number from an API pull_request_url
// (.../pulls/<n>), or 0 when absent.
func prNumberFromAPIURL(apiURL string) int {
	i := strings.LastIndex(apiURL, "/")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(apiURL[i+1:])
	return n
}

// findRelatedIssue scans a PR body for the issue it originates from: the first
// closing-keyword reference ("Closes #42") wins, else the first plain "#N" that
// is not the PR itself. Returns "" when no reference is found.
func findRelatedIssue(body string, prNumber int) string {
	if m := closingRefRe.FindStringSubmatch(body); len(m) == 2 {
		return m[1]
	}
	for _, m := range issueRefRe.FindAllStringSubmatch(body, -1) {
		if n, _ := strconv.Atoi(m[1]); n != 0 && n != prNumber {
			return m[1]
		}
	}
	return ""
}
