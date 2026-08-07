package prometheus

import "time"

// alert is one entry of Alertmanager's GET /api/v2/alerts response
// (openapi GettableAlert). Only the fields the adapter consumes are declared.
type alert struct {
	Fingerprint  string            `json:"fingerprint"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	GeneratorURL string            `json:"generatorURL"`
	Status       alertStatus       `json:"status"`
}

type alertStatus struct {
	State       string   `json:"state"` // "active", "suppressed", "unprocessed"
	SilencedBy  []string `json:"silencedBy"`
	InhibitedBy []string `json:"inhibitedBy"`
}

// alertGroup is one entry of GET /api/v2/alerts/groups (openapi AlertGroup):
// the alerts Alertmanager has grouped together by the routing tree's group_by
// labels, which is the unit an on-call actually gets paged about.
type alertGroup struct {
	Labels   map[string]string `json:"labels"`
	Receiver groupReceiver     `json:"receiver"`
	Alerts   []alert           `json:"alerts"`

	// alerts holds the members left after resolved ones are filtered out. It
	// is set by the adapter, never decoded from the API.
	alerts []alert
}

type groupReceiver struct {
	Name string `json:"name"`
}

// silence is the POST /api/v2/silences request body (openapi PostableSilence).
// Every matcher must hold for an alert to be suppressed.
type silence struct {
	Matchers  []matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// matcher is one label condition of a silence. The adapter only ever emits
// exact equality on the alert's own labels (IsRegex and IsEqual defaults),
// which pins the silence to a single alert.
type matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// silenceResponse is the POST /api/v2/silences response body.
type silenceResponse struct {
	SilenceID string `json:"silenceID"`
}
