package github

type issue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Labels      []label   `json:"labels"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	User        user      `json:"user"`
}

type user struct {
	Login string `json:"login"`
}

type label struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type issueRequest struct {
	State  string   `json:"state,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type commentRequest struct {
	Body string `json:"body"`
}

// comment is a GitHub issue comment, fetched by PollTask for approval steps.
type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type labelListRequest struct {
	Labels []string `json:"labels"`
}

type labelCreateRequest struct {
	Name string `json:"name"`
}

// PR (pull request) details for checking CI status.
type pullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// commitStatus is the combined legacy status of a commit. TotalCount is 0 when no
// legacy commit statuses exist — in which case State defaults to "pending" and must
// be ignored (a GitHub-Actions-only repo reports CI via check runs, not statuses).
type commitStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
	Statuses   []struct {
		Context string `json:"context"`
		State   string `json:"state"`
		URL     string `json:"target_url"`
	} `json:"statuses"`
}

// checkRunsResponse contains check runs for a commit.
type checkRunsResponse struct {
	CheckRuns []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	} `json:"check_runs"`
}
