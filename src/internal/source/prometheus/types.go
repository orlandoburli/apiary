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
