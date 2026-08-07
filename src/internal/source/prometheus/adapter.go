// Package prometheus provides a read-only source adapter that polls
// Prometheus Alertmanager (GET /api/v2/alerts) and maps each firing alert to
// an Apiary SourceItem, so workflow trigger matching (labels, states,
// title_regex) works against operational signals exactly as it does against
// tickets.
//
// Alerts are read-only work items: the adapter deliberately implements none of
// the optional write capabilities (StateSetter, LabelAdder, TaskPoller,
// CIStatusPoller, SubIssueCreator…). WriteResult is a no-op and Acknowledge is
// one too by default; config validation rejects workflows that need write
// capabilities against a source that lacks them (config.SourceCapabilities).
//
// The one opt-in write is ack_via_silence: with it enabled, acknowledging a
// dispatched alert creates a time-boxed Alertmanager silence for that alert, so
// it stops paging while an agent investigates.
package prometheus

import (
	"context"
	"fmt"
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

	// defaultSilenceDuration bounds an ack_via_silence silence. It is a
	// deliberate ceiling, not an estimate of how long an investigation runs:
	// if the agent dies or the daemon is killed the silence must expire on its
	// own, or a still-firing alert would stay invisible forever.
	defaultSilenceDuration = 2 * time.Hour

	// resolveConfirmations is how many consecutive checks must agree that an
	// alert is gone before ResolvedItems reports it. Two is the smallest value
	// that survives a single transient empty listing.
	resolveConfirmations = 2
)

// Adapter implements source.Adapter for Prometheus Alertmanager.
type Adapter struct {
	id     string
	client *client

	maxNewPerPoll int
	minAge        time.Duration

	// ackViaSilence turns Acknowledge from a no-op into "create an
	// Alertmanager silence for this alert", so a dispatched alert stops
	// re-notifying the on-call while an agent investigates it.
	ackViaSilence   bool
	silenceDuration time.Duration

	// byGroup dispatches one task per Alertmanager group instead of one per
	// alert, so a single incident that fans out into a dozen alerts becomes a
	// single investigation.
	byGroup bool

	// groupEpoch pins each live group's cycle-start (see groupItemID). Entries
	// are dropped when the group empties, which ends the cycle.
	groupEpoch map[string]time.Time

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

	// labels caches the exact label set of every surfaced alert, keyed by item
	// ID. Acknowledge needs it to build silence matchers: the SourceItem it is
	// handed is reconstructed from a source binding and carries no Metadata,
	// so the raw label map is not otherwise reachable. Pruned with seen.
	labels map[string]map[string]string

	// resolveStreak counts consecutive resolution checks in which an item
	// looked resolved. Reporting on the first check would make a single
	// unlucky moment — an Alertmanager that just restarted and has not been
	// re-fed by Prometheus yet — read as "everything resolved" and interrupt
	// every running investigation at once. Requiring two consecutive checks
	// costs one poll interval of latency and removes that whole class of
	// false positive. Reset the moment the item is seen live again.
	resolveStreak map[string]int

	// silenced records the silence created for an item ID and when it expires,
	// so a second Acknowledge for the same fire cycle (re-dispatch, retried
	// step) does not stack a second silence. Entries are dropped once expired,
	// which also bounds the map.
	silenced map[string]silenceRecord
}

// silenceRecord is one live silence the adapter created.
type silenceRecord struct {
	id     string
	expiry time.Time
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

	if v, ok := cfg["dispatch_by"]; ok {
		s, isString := v.(string)
		if !isString {
			return fmt.Errorf("prometheus: config.dispatch_by must be \"alert\" or \"group\", got %v", v)
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "alert", "":
			a.byGroup = false
		case "group":
			a.byGroup = true
		default:
			return fmt.Errorf("prometheus: config.dispatch_by must be \"alert\" or \"group\", got %q", s)
		}
	}

	a.ackViaSilence, _ = cfg["ack_via_silence"].(bool)

	a.silenceDuration = defaultSilenceDuration
	if v, ok := cfg["silence_duration"]; ok {
		s, _ := v.(string)
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("prometheus: config.silence_duration must be a positive duration (e.g. \"2h\"), got %v", v)
		}
		a.silenceDuration = d
	}

	a.seen = map[string]struct{}{}
	a.labels = map[string]map[string]string{}
	a.silenced = map[string]silenceRecord{}
	a.resolveStreak = map[string]int{}
	a.groupEpoch = map[string]time.Time{}

	aplog.Info("prometheus: configured  alertmanager=%s  max_new_per_poll=%d  min_age=%s  ack_via_silence=%v  silence_duration=%s",
		baseURL, a.maxNewPerPoll, a.minAge, a.ackViaSilence, a.silenceDuration)
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
	if a.byGroup {
		return a.pollGroups(ctx)
	}

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
		a.labels[id] = al.Labels
		items = append(items, a.toSourceItem(al))
	}
	if dropped > 0 {
		aplog.Warn("prometheus: storm cap — %d new alert(s) deferred to next poll (max_new_per_poll=%d)", dropped, a.maxNewPerPoll)
	}

	// Prune fire cycles that ended so a re-fire (new startsAt) counts as new.
	// An alert silenced by ack_via_silence also drops out here (silenced
	// alerts are excluded from the poll); its persisted task identity, not
	// this map, is what keeps it from re-dispatching when the silence lapses.
	for id := range a.seen {
		if _, ok := current[id]; !ok {
			delete(a.seen, id)
			delete(a.labels, id)
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

// Acknowledge is a no-op by default: an alert has no assignable/in-progress
// state to set. With ack_via_silence enabled it instead creates an Alertmanager
// silence pinned to the dispatched alert's exact label set, so the alert stops
// paging the on-call for as long as an agent is investigating it.
//
// Only the in_progress action silences. A skip means the item was never
// dispatched, and suppressing an alert nobody is looking at would hide it.
func (a *Adapter) Acknowledge(ctx context.Context, cell model.SourceItem, action model.AckAction) error {
	if !a.ackViaSilence || action != model.AckActionInProgress {
		aplog.Debug("prometheus: acknowledge %s (%s) — no-op for alerts", cell.LogLabel(), action)
		return nil
	}

	now := time.Now()
	matchers, ok := a.silenceMatchers(cell, now)
	if !ok {
		return nil // already silenced, or nothing safe to match on
	}

	endsAt := now.Add(a.silenceDuration)
	id, err := a.client.createSilence(ctx, silence{
		Matchers:  matchers,
		StartsAt:  now,
		EndsAt:    endsAt,
		CreatedBy: "apiary",
		Comment:   fmt.Sprintf("Apiary is investigating this alert (task %s). Expires automatically.", cell.ID),
	})
	if err != nil {
		return fmt.Errorf("prometheus: silencing %s: %w", cell.LogLabel(), err)
	}

	a.mu.Lock()
	a.silenced[cell.ID] = silenceRecord{id: id, expiry: endsAt}
	a.mu.Unlock()

	aplog.Info("prometheus: silenced %s until %s (silence=%s, %d matcher(s))",
		cell.LogLabel(), endsAt.UTC().Format(time.RFC3339), id, len(matchers))
	return nil
}

// ResolvedItems reports which of the given item IDs correspond to alerts that
// are no longer firing. It backs the source's interrupt_on_resolve policy, so a
// false positive kills a live investigation — the implementation is
// deliberately conservative in three ways:
//
//   - it queries Alertmanager for *every* alert, including silenced and
//     inhibited ones, so a suppressed alert is never mistaken for a resolved
//     one (an ack_via_silence silence would otherwise resolve its own alert);
//   - an API error is returned, never interpreted as "everything resolved";
//   - an item must look resolved on two consecutive checks before it is
//     reported, so one bad moment (an Alertmanager that just restarted with an
//     empty alert set) cannot interrupt every running investigation at once.
//
// An alert counts as gone when it is absent from the listing entirely, or is
// present with endsAt in the past — how a resolved alert lingers until
// Alertmanager's resolve_timeout drops it.
func (a *Adapter) ResolvedItems(ctx context.Context, itemIDs []string) ([]string, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}

	// In group mode the in-flight ids are group ids, so the live set must be
	// built from groups too — comparing them against per-alert ids would find
	// no match for anything and report every running investigation resolved.
	if a.byGroup {
		groups, err := a.client.allAlertGroups(ctx)
		if err != nil {
			return nil, fmt.Errorf("prometheus: checking resolved alert groups: %w", err)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.confirmResolved(itemIDs, a.groupLiveIDs(groups)), nil
	}

	alerts, err := a.client.allAlerts(ctx)
	if err != nil {
		return nil, fmt.Errorf("prometheus: checking resolved alerts: %w", err)
	}

	now := time.Now()
	live := make(map[string]struct{}, len(alerts))
	for _, al := range alerts {
		if al.Fingerprint == "" {
			continue
		}
		if !al.EndsAt.IsZero() && al.EndsAt.Before(now) {
			continue // resolved, still within resolve_timeout
		}
		live[itemID(al)] = struct{}{}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	return a.confirmResolved(itemIDs, live), nil
}

// confirmResolved applies the debounce: an item absent from the live set is
// only reported once it has been absent on resolveConfirmations consecutive
// checks. Callers must hold a.mu.
func (a *Adapter) confirmResolved(itemIDs []string, live map[string]struct{}) []string {
	// Only the ids we were asked about are tracked, so the streak map cannot
	// outgrow the set of in-flight investigations.
	asked := make(map[string]struct{}, len(itemIDs))
	var resolved []string
	for _, id := range itemIDs {
		asked[id] = struct{}{}
		if _, ok := live[id]; ok {
			delete(a.resolveStreak, id)
			continue
		}
		a.resolveStreak[id]++
		if a.resolveStreak[id] < resolveConfirmations {
			aplog.Debug("prometheus: %s looks resolved (%d/%d confirmations) — waiting for the next check",
				id, a.resolveStreak[id], resolveConfirmations)
			continue
		}
		resolved = append(resolved, id)
	}
	for id := range a.resolveStreak {
		if _, ok := asked[id]; !ok {
			delete(a.resolveStreak, id)
		}
	}
	return resolved
}

// silenceMatchers builds the exact-equality matchers for one alert and reports
// whether a silence should be created at all (false when this fire cycle is
// already silenced, or when no label set could be recovered).
//
// The label set comes from the poll-time cache, which holds the alert's raw
// labels. When the daemon restarted between the poll and this call the cache is
// cold, so it falls back to the item's "key:value" labels — lossless here
// because a Prometheus label name can never contain a colon, so splitting on
// the first one reproduces the original pair.
func (a *Adapter) silenceMatchers(cell model.SourceItem, now time.Time) ([]matcher, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for id, rec := range a.silenced {
		if !rec.expiry.After(now) {
			delete(a.silenced, id)
		}
	}
	if rec, ok := a.silenced[cell.ID]; ok {
		aplog.Debug("prometheus: %s already silenced until %s (silence=%s) — skipping",
			cell.LogLabel(), rec.expiry.UTC().Format(time.RFC3339), rec.id)
		return nil, false
	}

	labels := a.labels[cell.ID]
	if len(labels) == 0 {
		labels = map[string]string{}
		for _, l := range cell.Labels {
			if k, v, found := strings.Cut(l, ":"); found {
				labels[k] = v
			}
		}
	}
	if len(labels) == 0 {
		aplog.Warn("prometheus: cannot silence %s — no label set available; leaving the alert unsilenced", cell.LogLabel())
		return nil, false
	}

	matchers := make([]matcher, 0, len(labels))
	for _, k := range sortedKeys(labels) {
		matchers = append(matchers, matcher{Name: k, Value: labels[k], IsEqual: true})
	}
	return matchers, true
}

// WriteResult is a no-op: Alertmanager has no per-alert comment surface. The
// intended pattern is a workflow step that publishes findings to a ticket
// source (APIARY_PUBLISH / APIARY_SPAWN) instead.
func (a *Adapter) WriteResult(_ context.Context, cell model.SourceItem, result model.RunResult) error {
	aplog.Debug("prometheus: write result for %s (success=%v) — no-op for alerts", cell.LogLabel(), result.Success)
	return nil
}

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
