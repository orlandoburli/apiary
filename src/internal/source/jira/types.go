package jira

import "encoding/json"

// searchResponse is the envelope of GET /rest/api/3/search/jql. The enhanced
// endpoint paginates with an opaque token and carries no total count.
type searchResponse struct {
	Issues        []issue `json:"issues"`
	NextPageToken string  `json:"nextPageToken"`
}

type issue struct {
	ID     string      `json:"id"`  // immutable numeric id, e.g. "10042"
	Key    string      `json:"key"` // human-facing key, e.g. "ERP-42"
	Fields issueFields `json:"fields"`
}

type issueFields struct {
	Summary     string          `json:"summary"`
	Description json.RawMessage `json:"description"` // ADF document, or null
	Status      *statusEntity   `json:"status"`
	Priority    *namedEntity    `json:"priority"`
	IssueType   *namedEntity    `json:"issuetype"`
	Labels      []string        `json:"labels"`
	Created     string          `json:"created"`
	Updated     string          `json:"updated"`
}

type namedEntity struct {
	Name string `json:"name"`
}

type statusEntity struct {
	Name           string         `json:"name"`
	StatusCategory statusCategory `json:"statusCategory"`
}

type statusCategory struct {
	Key string `json:"key"` // "new", "indeterminate", "done"
}

// issueLinks is the slice of GET /issue/{id}?fields=issuelinks we care about:
// the issue's links, each carrying the link type and the OTHER side's issue —
// inwardIssue when the other issue points at this one (for a "Blocks" link,
// the inward side is "is blocked by", i.e. a blocker of this issue).
type issueLinks struct {
	Fields struct {
		IssueLinks []issueLink `json:"issuelinks"`
	} `json:"fields"`
}

type issueLink struct {
	Type        issueLinkType `json:"type"`
	InwardIssue *linkedIssue  `json:"inwardIssue"`
}

type issueLinkType struct {
	Name string `json:"name"` // e.g. "Blocks"
}

type linkedIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string        `json:"summary"`
		Status  *statusEntity `json:"status"`
	} `json:"fields"`
}

// transitionsResponse is the envelope of GET /issue/{id}/transitions.
type transitionsResponse struct {
	Transitions []transition `json:"transitions"`
}

type transition struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	To   statusEntity `json:"to"`
}

type transitionRequest struct {
	Transition transitionID `json:"transition"`
}

type transitionID struct {
	ID string `json:"id"`
}

// commentPage is the envelope of GET /issue/{id}/comment (offset pagination).
type commentPage struct {
	Comments []comment `json:"comments"`
	Total    int       `json:"total"`
}

type comment struct {
	ID      string          `json:"id"`
	Body    json.RawMessage `json:"body"` // ADF document
	Created string          `json:"created"`
}

type commentCreateRequest struct {
	Body adfNode `json:"body"`
}

// labelsUpdateRequest is the PUT /issue/{id} body using Jira's atomic update
// verbs, so labels are added/removed without a read-modify-write cycle.
type labelsUpdateRequest struct {
	Update labelsUpdate `json:"update"`
}

type labelsUpdate struct {
	Labels []labelOp `json:"labels"`
}

type labelOp struct {
	Add    string `json:"add,omitempty"`
	Remove string `json:"remove,omitempty"`
}

// myself is the slice of GET /rest/api/3/myself we care about.
type myself struct {
	TimeZone string `json:"timeZone"`
}
