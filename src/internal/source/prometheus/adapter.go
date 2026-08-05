// Package prometheus provides a read-only source adapter that polls
// Prometheus Alertmanager (GET /api/v2/alerts) and maps each firing alert to
// an Apiary SourceItem, so workflow trigger matching (labels, states,
// title_regex) works against operational signals exactly as it does against
// tickets.
//
// Alerts are read-only work items: the adapter deliberately implements none of
// the optional write capabilities (StateSetter, LabelAdder, TaskPoller,
// CIStatusPoller, SubIssueCreator…). Acknowledge and WriteResult are no-ops;
// config validation rejects workflows that need write capabilities against a
// source that lacks them (config.SourceCapabilities).
package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("prometheus", func() source.Adapter { return &Adapter{} })
}

const (
	// defaultMaxNewPerPoll caps how many not-yet-seen alerts one poll may
	// surface — the alert-storm guardrail. One bad deploy can fire dozens of
	// alerts at once; without a cap each becomes a task + agent run in the same
	// tick. Overflow alerts are logged and surface on later polls.
	defaultMaxNewPerPoll = 10

	// defaultMinAge is the flap dampener: an alert must have been firing at
	// least this long before it is surfaced. Complements the alerting rule's
	// own `for:` clause; 0 would dispatch on the very first evaluation.
	defaultMinAge = time.Minute
)

// Adapter implements source.Adapter for Prometheus Alertmanager.
type Adapter struct {
	id     string
	client *client

	maxNewPerPoll int
	minAge        time.Duration

	// filters are Alertmanager matcher strings sent as filter= params,
	// built from SourceFilters.Labels.
	filters []string

	// seen tracks alert item IDs (fingerprint:startsAt) already surfaced, so
	// the storm cap only counts genuinely new alerts and an ongoing alert is
	// re-returned every poll (the dispatcher's active/once dedup relies on
	// that). Entries are pruned when the alert stops firing. In-memory only:
	// after a restart every firing alert counts as new again, but re-dispatch
	// is still prevented downstream by the persisted task/instance dedup.
	mu   sync.Mutex
	seen map[string]struct{}
}

func (a *Adapter) ID() string { return a.id }

// SetID sets the source ID for this adapter.
func (a *Adapter) SetID(id string) { a.id = id }

// Connect validates config and creates the HTTP client.
func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	baseURL, _ := cfg["alertmanager_url"].(string)
	if baseURL == "" {
		return fmt.Errorf("prometheus: config.alertmanager_url is required")
	}

	bearer, _ := cfg["bearer_token"].(string)
	basicUser, _ := cfg["basic_auth_user"].(string)
	basicPass, _ := cfg["basic_auth_password"].(string)
	a.client = newClient(baseURL, bearer, basicUser, basicPass)

	a.maxNewPerPoll = defaultMaxNewPerPoll
	if v, ok := cfg["max_new_per_poll"]; ok {
		n, err := toInt(v)
		if err != nil || n < 0 {
			return fmt.Errorf("prometheus: config.max_new_per_poll must be a non-negative integer, got %v", v)
		}
		a.maxNewPerPoll = n
	}

	a.minAge = defaultMinAge
	if v, ok := cfg["min_age"]; ok {
		s, _ := v.(string)
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return fmt.Errorf("prometheus: config.min_age must be a non-negative duration (e.g. \"2m\"), got %v", v)
		}
		a.minAge = d
	}

	a.seen = map[string]struct{}{}

	aplog.Info("prometheus: configured  alertmanager=%s  max_new_per_poll=%d  min_age=%s",
		baseURL, a.maxNewPerPoll, a.minAge)
	return nil
}

// SetFilters maps SourceFilters.Labels to Alertmanager matcher strings.
// Entries may be "key=value", "key:value", or a raw matcher ("key=~\"re\"").
// States are not sent to Alertmanager (only firing alerts are polled); a
// states filter other than "firing" is rejected at Connect-time semantics
// here to avoid a filter that silently never matches.
func (a *Adapter) SetFilters(states, labels []string) {
	for _, l := range labels {
		a.filters = append(a.filters, toMatcher(l))
	}
	for _, s := range states {
		if !strings.EqualFold(s, "firing") {
			aplog.Warn("prometheus: filters.states %q ignored — only firing alerts are polled", s)
		}
	}
}

// matcherRe recognises a raw Alertmanager matcher with an explicit operator
// and quoted value, which is passed through untouched.
var matcherRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*\s*(=~|!~|!=|=)\s*".*"$`)

// toMatcher normalises a filters.labels entry to an Alertmanager matcher.
func toMatcher(l string) string {
	l = strings.TrimSpace(l)
	if matcherRe.MatchString(l) {
		return l
	}
	if k, v, ok := strings.Cut(l, "="); ok {
		return fmt.Sprintf("%s=%q", strings.TrimSpace(k), strings.TrimSpace(v))
	}
	if k, v, ok := strings.Cut(l, ":"); ok {
		return fmt.Sprintf("%s=%q", strings.TrimSpace(k), strings.TrimSpace(v))
	}
	// A bare word is most useful as an alertname match.
	return fmt.Sprintf("alertname=%q", l)
}

// Poll returns the currently firing alerts as SourceItems. The since parameter
// is ignored on purpose: an ongoing alert must be returned on every poll so
// the dispatcher's active-instance / once dedup keeps shadowing it; item IDs
// (fingerprint:startsAt) make re-dispatch impossible while a fire cycle lasts.
func (a *Adapter) Poll(ctx context.Context, _ time.Time) ([]model.SourceItem, error) {
	alerts, err := a.client.alerts(ctx, a.filters)
	if err != nil {
		return nil, fmt.Errorf("prometheus: polling alerts: %w", err)
	}

	now := time.Now()
	var eligible []alert
	for _, al := range alerts {
		if al.Fingerprint == "" {
			aplog.Debug("prometheus: skipping alert without fingerprint (labels=%v)", al.Labels)
			continue
		}
		// Flap dampener: too-young alerts are left for a later poll (not marked
		// seen), so firing→resolved→firing blips shorter than min_age never
		// become tasks.
		if age := now.Sub(al.StartsAt); age < a.minAge {
			aplog.Debug("prometheus: alert %s age %s < min_age %s — deferred", al.Fingerprint, age.Round(time.Second), a.minAge)
			continue
		}
		eligible = append(eligible, al)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Oldest first so a storm drains deterministically.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].StartsAt.Before(eligible[j].StartsAt) })

	current := make(map[string]struct{}, len(eligible))
	var items []model.SourceItem
	newCount := 0
	dropped := 0
	for _, al := range eligible {
		id := itemID(al)
		current[id] = struct{}{}
		if _, ok := a.seen[id]; !ok {
			if a.maxNewPerPoll > 0 && newCount >= a.maxNewPerPoll {
				dropped++
				delete(current, id) // not surfaced: stays unseen for the next poll
				continue
			}
			newCount++
			a.seen[id] = struct{}{}
		}
		items = append(items, a.toSourceItem(al))
	}
	if dropped > 0 {
		aplog.Warn("prometheus: storm cap — %d new alert(s) deferred to next poll (max_new_per_poll=%d)", dropped, a.maxNewPerPoll)
	}

	// Prune fire cycles that ended so a re-fire (new startsAt) counts as new.
	for id := range a.seen {
		if _, ok := current[id]; !ok {
			delete(a.seen, id)
		}
	}

	return items, nil
}

// itemID is the stable per-fire-cycle identity: the Alertmanager fingerprint
// (hash of the label set) plus startsAt. While an alert keeps firing the ID is
// constant — the dispatcher dedups it; after resolve+re-fire startsAt changes
// and the alert dispatches again as a new item.
func itemID(al alert) string {
	return al.Fingerprint + ":" + al.StartsAt.UTC().Format(time.RFC3339)
}

func (a *Adapter) toSourceItem(al alert) model.SourceItem {
	name := al.Labels["alertname"]
	if name == "" {
		name = "alert " + al.Fingerprint
	}
	title := name
	if s := al.Annotations["summary"]; s != "" {
		title = fmt.Sprintf("%s: %s", name, s)
	}

	// Alert labels become routable "key:value" item labels (the router
	// lowercases both sides), e.g. trigger match `labels: [severity:critical]`.
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
		ID:          itemID(al),
		SourceID:    a.ID(),
		Number:      number,
		Title:       title,
		Description: describe(al),
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
			"status":       al.Status.State,
		},
		CreatedAt: al.StartsAt,
		UpdatedAt: al.UpdatedAt,
	}
}

// describe renders the full alert payload as the task description, so the
// investigating agent receives labels, annotations, and the generator link
// without needing Alertmanager access of its own.
func describe(al alert) string {
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

	extra := sortedKeys(al.Annotations)
	wrote := false
	for _, k := range extra {
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

// Acknowledge is a no-op: an alert has no assignable/in-progress state to set.
// Silencing on dispatch (ack_via_silence) is a possible future opt-in.
func (a *Adapter) Acknowledge(_ context.Context, cell model.SourceItem, action model.AckAction) error {
	aplog.Debug("prometheus: acknowledge %s (%s) — no-op for alerts", cell.LogLabel(), action)
	return nil
}

// WriteResult is a no-op: Alertmanager has no per-alert comment surface. The
// intended pattern is a workflow step that publishes findings to a ticket
// source (APIARY_PUBLISH / APIARY_SPAWN) instead.
func (a *Adapter) WriteResult(_ context.Context, cell model.SourceItem, result model.RunResult) error {
	aplog.Debug("prometheus: write result for %s (success=%v) — no-op for alerts", cell.LogLabel(), result.Success)
	return nil
}

func (a *Adapter) WebhookHandler() http.Handler { return nil }

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	}
	return 0, fmt.Errorf("not an integer: %v", v)
}
