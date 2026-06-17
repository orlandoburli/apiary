package codeberg

// issue is a Forgejo/Gitea issue. The /issues endpoint returns pull requests too
// (a PR is an issue in the API); PullRequest is non-nil for those.
type issue struct {
	ID          int64            `json:"id"`
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	State       string           `json:"state"`
	Labels      []label          `json:"labels"`
	HTMLURL     string           `json:"html_url"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	PullRequest *pullRequestMeta `json:"pull_request,omitempty"`
}

// pullRequestMeta is the marker Forgejo attaches to an issue object that is
// actually a pull request. Merged lets ListBlockers read a blocker's merge state
// without a second round-trip when the blocker is itself a PR.
type pullRequestMeta struct {
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
}

type label struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// editIssueRequest patches an issue. Forgejo's EditIssueOption.state is a
// *string, so it is omitted unless set.
type editIssueRequest struct {
	State string `json:"state,omitempty"`
}

// createIssueRequest is the body for POST /repos/{o}/{r}/issues. Forgejo's
// CreateIssueOption.labels is []int64 (ids only) — unlike the add-labels
// endpoint, which also accepts names. We always work in ids.
type createIssueRequest struct {
	Title  string  `json:"title"`
	Body   string  `json:"body,omitempty"`
	Labels []int64 `json:"labels,omitempty"`
}

// issueLabelsRequest adds labels to an issue. Recent Forgejo accepts ids or
// names; we always send ids for cross-version safety (older versions reject
// names with a JSON unmarshal error).
type issueLabelsRequest struct {
	Labels []int64 `json:"labels"`
}

// createLabelRequest creates a repo label. Forgejo requires both name and color.
type createLabelRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type commentRequest struct {
	Body string `json:"body"`
}

// comment is a Forgejo issue comment, fetched by PollTask for approval steps.
type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// pullRequest carries the merge-conflict and head-SHA signals for CI waits.
// Forgejo exposes Mergeable as a bool (there is no GitHub-style mergeable_state
// string): Mergeable == false on an unmerged PR means it has conflicts and can
// never merge. Forgejo computes mergeability lazily, so a freshly pushed PR can
// report a stale value for a moment — the wait_for step's on_conflict edge
// (own retry budget) absorbs that.
type pullRequest struct {
	Number    int    `json:"number"`
	HTMLURL   string `json:"html_url"`
	Mergeable bool   `json:"mergeable"`
	Merged    bool   `json:"merged"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// combinedStatus is Forgejo's combined commit status. Forgejo has no GitHub-style
// check-runs API: Forgejo Actions and any external CI both report through these
// commit statuses, so the combined endpoint is the single CI signal. Note the
// top-level field is "state" while each entry's field is "status".
type combinedStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
	Statuses   []struct {
		Context   string `json:"context"`
		Status    string `json:"status"`
		TargetURL string `json:"target_url"`
	} `json:"statuses"`
}

// timelineComment is one entry of an issue's timeline. A pull request that
// references the issue surfaces as RefIssue with its PullRequest marker set —
// Forgejo has no separate ref_pull_request field because a PR is an issue.
type timelineComment struct {
	Type     string `json:"type"`
	RefIssue *issue `json:"ref_issue"`
}
