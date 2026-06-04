package github

type issue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	Labels    []label    `json:"labels"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	User      user       `json:"user"`
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

type labelListRequest struct {
	Labels []string `json:"labels"`
}

type labelCreateRequest struct {
	Name string `json:"name"`
}

type reviewRequest struct {
	Event string `json:"event"`
	Body  string `json:"body"`
}
