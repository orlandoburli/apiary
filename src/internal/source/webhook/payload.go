package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// parse maps a delivery body to SourceItems per the configured format.
func (a *Adapter) parse(body []byte) ([]model.SourceItem, error) {
	switch a.format {
	case "alertmanager":
		return a.parseAlertmanager(body)
	default:
		return a.parseGeneric(body)
	}
}

// genericEvent is the payload shape for format: "generic". Every field is
// optional; unknown fields are preserved in Metadata via the raw payload.
type genericEvent struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Labels      json.RawMessage `json:"labels"` // {"k":"v"} object or ["k:v"] array
	Priority    string          `json:"priority"`
	Severity    string          `json:"severity"`
	State       string          `json:"state"`
	URL         string          `json:"url"`
}

// parseGeneric maps one JSON object (or an {"events": [...]} / top-level
// array batch) to SourceItems. Identity: the event's own id when given,
// otherwise a hash of its JSON — so a sender that retries the same body never
// dispatches twice, while distinct payloads always get distinct IDs.
func (a *Adapter) parseGeneric(body []byte) ([]model.SourceItem, error) {
	trimmed := strings.TrimSpace(string(body))
	var raws []json.RawMessage
	switch {
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal(body, &raws); err != nil {
			return nil, fmt.Errorf("invalid JSON array: %w", err)
		}
	default:
		var batch struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		if batch.Events != nil {
			raws = batch.Events
		} else {
			raws = []json.RawMessage{json.RawMessage(body)}
		}
	}

	items := make([]model.SourceItem, 0, len(raws))
	for _, raw := range raws {
		var ev genericEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("invalid event object: %w", err)
		}
		items = append(items, a.genericItem(ev, raw))
	}
	return items, nil
}

func (a *Adapter) genericItem(ev genericEvent, raw json.RawMessage) model.SourceItem {
	id := ev.ID
	if id == "" {
		sum := sha256.Sum256(raw)
		id = hex.EncodeToString(sum[:])[:16]
	}

	title := ev.Title
	if title == "" {
		title = ev.Summary
	}
	if title == "" {
		title = "webhook event " + id
	}

	labels := parseLabels(ev.Labels)

	priority := ev.Priority
	if priority == "" {
		priority = ev.Severity
	}

	state := ev.State
	if state == "" {
		state = "open"
	}

	number := id
	if len(number) > 7 {
		number = number[:7]
	}

	var meta map[string]any
	_ = json.Unmarshal(raw, &meta)

	now := time.Now()
	return model.SourceItem{
		ID:          id,
		SourceID:    a.ID(),
		Number:      number,
		Title:       title,
		Description: describeGeneric(ev, labels, raw),
		Labels:      labels,
		Type:        "webhook",
		Priority:    priority,
		State:       state,
		URL:         ev.URL,
		Metadata:    map[string]any{"payload": meta},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// parseLabels accepts either an object ({"k":"v"}) or an array (["k:v",
// "k=v", "bare"]) and normalizes to the router's "key:value" form.
func parseLabels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var asMap map[string]string
	if err := json.Unmarshal(raw, &asMap); err == nil {
		labels := make([]string, 0, len(asMap))
		for k, v := range asMap {
			labels = append(labels, k+":"+v)
		}
		sort.Strings(labels)
		return labels
	}
	var asList []string
	if err := json.Unmarshal(raw, &asList); err == nil {
		labels := make([]string, 0, len(asList))
		for _, l := range asList {
			if k, v, ok := strings.Cut(l, "="); ok {
				l = strings.TrimSpace(k) + ":" + strings.TrimSpace(v)
			}
			labels = append(labels, l)
		}
		sort.Strings(labels)
		return labels
	}
	aplog.Debug("webhook: labels field is neither an object nor a string array — ignored")
	return nil
}

func describeGeneric(ev genericEvent, labels []string, raw json.RawMessage) string {
	var b strings.Builder
	if ev.Summary != "" && ev.Summary != ev.Title {
		b.WriteString(ev.Summary)
		b.WriteString("\n\n")
	}
	if ev.Description != "" {
		b.WriteString(ev.Description)
		b.WriteString("\n\n")
	}
	if len(labels) > 0 {
		b.WriteString("**Labels**\n\n")
		for _, l := range labels {
			fmt.Fprintf(&b, "- `%s`\n", l)
		}
		b.WriteString("\n")
	}
	if ev.URL != "" {
		fmt.Fprintf(&b, "Source: %s\n\n", ev.URL)
	}
	b.WriteString("**Payload**\n\n```json\n")
	b.Write(compactJSON(raw))
	b.WriteString("\n```")
	return b.String()
}

func compactJSON(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return pretty
}

// amPayload is the Alertmanager webhook payload (version "4"), as sent by
// webhook_configs. Unlike the GET /api/v2/alerts shape the poll adapter
// consumes, alerts arrive grouped and carry a per-alert status string.
type amPayload struct {
	Version string    `json:"version"`
	Status  string    `json:"status"` // group status: firing | resolved
	Alerts  []amAlert `json:"alerts"`
}

type amAlert struct {
	Status       string            `json:"status"` // firing | resolved
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// parseAlertmanager maps each firing alert in the delivery to a SourceItem
// with the same identity scheme as the prometheus poll adapter
// (fingerprint:startsAt — stable per fire cycle), so switching a source
// between poll and push keeps dedup semantics. Resolved alerts are skipped:
// the resolved-while-running policy is a separate opt-in (#362).
func (a *Adapter) parseAlertmanager(body []byte) ([]model.SourceItem, error) {
	var p amPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("invalid Alertmanager payload: %w", err)
	}
	if p.Alerts == nil {
		return nil, fmt.Errorf("invalid Alertmanager payload: missing alerts array")
	}

	var items []model.SourceItem
	for _, al := range p.Alerts {
		if !strings.EqualFold(al.Status, "firing") {
			aplog.Debug("webhook %s: skipping %s alert %s", a.id, al.Status, al.Fingerprint)
			continue
		}
		if al.Fingerprint == "" {
			aplog.Debug("webhook %s: skipping alert without fingerprint (labels=%v)", a.id, al.Labels)
			continue
		}
		items = append(items, a.alertItem(al))
	}
	return items, nil
}

func (a *Adapter) alertItem(al amAlert) model.SourceItem {
	name := al.Labels["alertname"]
	if name == "" {
		name = "alert " + al.Fingerprint
	}
	title := name
	if s := al.Annotations["summary"]; s != "" {
		title = fmt.Sprintf("%s: %s", name, s)
	}

	labels := make([]string, 0, len(al.Labels))
	for k, v := range al.Labels {
		labels = append(labels, k+":"+v)
	}
	sort.Strings(labels)

	number := al.Fingerprint
	if len(number) > 7 {
		number = number[:7]
	}

	return model.SourceItem{
		ID:          al.Fingerprint + ":" + al.StartsAt.UTC().Format(time.RFC3339),
		SourceID:    a.ID(),
		Number:      number,
		Title:       title,
		Description: describeAlert(al),
		Labels:      labels,
		Type:        "alert",
		Priority:    al.Labels["severity"],
		State:       "firing",
		URL:         al.GeneratorURL,
		Metadata: map[string]any{
			"fingerprint":  al.Fingerprint,
			"labels":       al.Labels,
			"annotations":  al.Annotations,
			"startsAt":     al.StartsAt.UTC().Format(time.RFC3339),
			"endsAt":       al.EndsAt.UTC().Format(time.RFC3339),
			"generatorURL": al.GeneratorURL,
			"status":       al.Status,
		},
		CreatedAt: al.StartsAt,
		UpdatedAt: al.StartsAt,
	}
}

// describeAlert renders the alert as the task description, mirroring the
// prometheus poll adapter so the investigating agent gets labels,
// annotations, and the generator link regardless of transport.
func describeAlert(al amAlert) string {
	var b strings.Builder
	if s := al.Annotations["summary"]; s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	if d := al.Annotations["description"]; d != "" {
		b.WriteString(d)
		b.WriteString("\n\n")
	}

	b.WriteString("**Labels**\n\n")
	for _, k := range sortedKeys(al.Labels) {
		fmt.Fprintf(&b, "- `%s`: `%s`\n", k, al.Labels[k])
	}

	wrote := false
	for _, k := range sortedKeys(al.Annotations) {
		if k == "summary" || k == "description" {
			continue
		}
		if !wrote {
			b.WriteString("\n**Annotations**\n\n")
			wrote = true
		}
		fmt.Fprintf(&b, "- `%s`: %s\n", k, al.Annotations[k])
	}

	fmt.Fprintf(&b, "\nFiring since %s.", al.StartsAt.UTC().Format(time.RFC3339))
	if al.GeneratorURL != "" {
		fmt.Fprintf(&b, "\nSource expression: %s", al.GeneratorURL)
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
