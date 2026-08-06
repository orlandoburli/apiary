package dynatrace

// problem is one entry of Dynatrace's GET /api/v2/problems response. Only the
// fields the adapter consumes are declared.
type problem struct {
	ProblemID        string      `json:"problemId"`
	DisplayID        string      `json:"displayId"`
	Title            string      `json:"title"`
	ImpactLevel      string      `json:"impactLevel"`   // INFRASTRUCTURE, SERVICES, APPLICATION, ENVIRONMENT
	SeverityLevel    string      `json:"severityLevel"` // AVAILABILITY, ERROR, PERFORMANCE, RESOURCE_CONTENTION, CUSTOM_ALERT, ...
	Status           string      `json:"status"`        // OPEN, RESOLVED, CLOSED
	StartTime        int64       `json:"startTime"`     // ms since epoch
	EndTime          int64       `json:"endTime"`       // ms since epoch, -1 while OPEN
	AffectedEntities []entityRef `json:"affectedEntities"`
	ImpactedEntities []entityRef `json:"impactedEntities"`
	RootCauseEntity  *entityRef  `json:"rootCauseEntity"`
	ManagementZones  []namedRef  `json:"managementZones"`
	ProblemFilters   []namedRef  `json:"problemFilters"`
	EntityTags       []entityTag `json:"entityTags"`
}

type entityRef struct {
	EntityID struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"entityId"`
	Name string `json:"name"`
}

type namedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type entityTag struct {
	Context              string `json:"context"`
	Key                  string `json:"key"`
	Value                string `json:"value"`
	StringRepresentation string `json:"stringRepresentation"`
}

// problemsPage is the paginated envelope of GET /api/v2/problems.
type problemsPage struct {
	TotalCount  int       `json:"totalCount"`
	PageSize    int       `json:"pageSize"`
	NextPageKey string    `json:"nextPageKey"`
	Problems    []problem `json:"problems"`
}
