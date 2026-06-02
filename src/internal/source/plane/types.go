package plane

// page is the generic paginated response envelope from the Plane API.
type page[T any] struct {
	Results         []T    `json:"results"`
	NextCursor      string `json:"next_cursor"`
	NextPageResults bool   `json:"next_page_results"` // false = no more pages
	TotalCount      int    `json:"total_count"`
}

// workItem represents a Plane work item (formerly "issue").
type workItem struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	SequenceID          int      `json:"sequence_id"`
	DescriptionStripped string   `json:"description_stripped"`
	DescriptionHTML     string   `json:"description_html"`
	Priority            string   `json:"priority"`
	State               string   `json:"state"`  // state UUID
	Labels              []string `json:"labels"` // label UUIDs
	TypeID              *string  `json:"type_id"`
	ProjectID           string   `json:"project_id"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
}

// state represents a Plane workflow state.
type state struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"` // backlog | unstarted | started | completed | cancelled
}

// label represents a Plane label.
type label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// commentRequest is the request body for POST .../comments/.
type commentRequest struct {
	CommentHTML string `json:"comment_html"`
}

// patchRequest is the request body for PATCH .../work-items/{id}/.
type patchRequest struct {
	State string `json:"state"`
}

// labelsPatchRequest is the request body for PATCH .../work-items/{id}/ when
// replacing the label set. Plane's PATCH replaces labels wholesale, so callers
// must send the full merged list of label UUIDs.
type labelsPatchRequest struct {
	Labels []string `json:"labels"`
}

// labelCreateRequest is the request body for POST .../labels/.
type labelCreateRequest struct {
	Name string `json:"name"`
}
