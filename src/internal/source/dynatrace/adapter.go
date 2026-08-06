// Package dynatrace provides a read-only source adapter that polls the
// Dynatrace problems API (GET /api/v2/problems) and maps each open problem to
// an Apiary SourceItem, so workflow trigger matching (labels, states,
// title_regex) works against operational signals exactly as it does against
// tickets — the monitoring-source shape introduced by the prometheus adapter,
// backed by a different client.
//
// Problems are read-only work items: the adapter deliberately implements none
// of the optional write capabilities (StateSetter, LabelAdder, TaskPoller,
// CIStatusPoller, SubIssueCreator…). Acknowledge and WriteResult are no-ops;
// config validation rejects workflows that need write capabilities against a
// source that lacks them (config.SourceCapabilities). Comment write-back via
// POST /api/v2/problems/{id}/comments (problems.write scope) is a possible
// future opt-in.
package dynatrace

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
	source.Register("dynatrace", func() source.Adapter { return &Adapter{} })
}

const (
	// defaultMaxNewPerPoll caps how many not-yet-seen problems one poll may
	// surface — the problem-storm guardrail. One bad deploy can open dozens of
	// problems at once; without a cap each becomes a task + agent run in the
	// same tick. Overflow problems are logged and surface on later polls.
	defaultMaxNewPerPoll = 10

	// defaultMinAge is the flap dampener: a problem must have been open at
	// least this long before it is surfaced. 0 would dispatch the moment
	// Dynatrace opens the problem.
	defaultMinAge = time.Minute

	// defaultLookback bounds the from= query parameter. The problems API
	// defaults to the last 2 hours, which would silently hide older still-open
	// problems, so the adapter always sends an explicit window.
	defaultLookback = 30 * 24 * time.Hour
)

// Adapter implements source.Adapter for Dynatrace problems.
type Adapter struct {
	id     string
	client *client

	maxNewPerPoll int
	minAge        time.Duration
	lookback      time.Duration

	// selector is the problemSelector criteria list built from
	// SourceFilters.Labels; status("open") is always appended at poll time.
	selector []string

	// seen tracks problem IDs already surfaced, so the storm cap only counts
	// genuinely new problems and an ongoing problem is re-returned every poll
	// (the dispatcher's active/once dedup relies on that). Entries are pruned
	// when the problem is no longer open. In-memory only: after a restart
	// every open problem counts as new again, but re-dispatch is still
	// prevented downstream by the persisted task/instance dedup.
	mu   sync.Mutex
	seen map[string]struct{}
}

func (a *Adapter) ID() string { return a.id }

// SetID sets the source ID for this adapter.
func (a *Adapter) SetID(id string) { a.id = id }

// Connect validates config and creates the HTTP client.
func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	baseURL, _ := cfg["base_url"].(string)
	if baseURL == "" {
		return fmt.Errorf("dynatrace: config.base_url is required")
	}
	apiToken, _ := cfg["api_token"].(string)
	if apiToken == "" {
		return fmt.Errorf("dynatrace: config.api_token is required (scope: problems.read)")
	}
	a.client = newClient(baseURL, apiToken)

	a.maxNewPerPoll = defaultMaxNewPerPoll
	if v, ok := cfg["max_new_per_poll"]; ok {
		n, err := toInt(v)
		if err != nil || n < 0 {
			return fmt.Errorf("dynatrace: config.max_new_per_poll must be a non-negative integer, got %v", v)
		}
		a.maxNewPerPoll = n
	}

	a.minAge = defaultMinAge
	if v, ok := cfg["min_age"]; ok {
		s, _ := v.(string)
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return fmt.Errorf("dynatrace: config.min_age must be a non-negative duration (e.g. \"2m\"), got %v", v)
		}
		a.minAge = d
	}

	a.lookback = defaultLookback
	if v, ok := cfg["lookback"]; ok {
		s, _ := v.(string)
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("dynatrace: config.lookback must be a positive duration (e.g. \"720h\"), got %v", v)
		}
		a.lookback = d
	}

	a.seen = map[string]struct{}{}

	aplog.Info("dynatrace: configured  base_url=%s  max_new_per_poll=%d  min_age=%s  lookback=%s",
		baseURL, a.maxNewPerPoll, a.minAge, a.lookback)
	return nil
}

// SetFilters maps SourceFilters.Labels to problemSelector criteria.
// Entries may be a raw criterion (`severityLevel("AVAILABILITY")`), a known
// key=value pair (severityLevel, impactLevel, managementZone), a generic
// key=value / key:value pair (matched as an entity tag), or a bare word
// (matched against the problem title). States are not sent to Dynatrace (only
// open problems are polled); a states filter other than "open" is warned
// about here to avoid a filter that silently never matches.
func (a *Adapter) SetFilters(states, labels []string) {
	for _, l := range labels {
		a.selector = append(a.selector, toCriterion(l))
	}
	for _, s := range states {
		if !strings.EqualFold(s, "open") {
			aplog.Warn("dynatrace: filters.states %q ignored — only open problems are polled", s)
		}
	}
}

// criterionRe recognises a raw problemSelector criterion with an explicit
// function-call shape, which is passed through untouched.
var criterionRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*\(.*\)$`)

// toCriterion normalises a filters.labels entry to a problemSelector criterion.
func toCriterion(l string) string {
	l = strings.TrimSpace(l)
	if criterionRe.MatchString(l) {
		return l
	}
	k, v, cut := strings.Cut(l, "=")
	if !cut {
		k, v, cut = strings.Cut(l, ":")
	}
	if cut {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "severitylevel", "severity":
			return fmt.Sprintf("severityLevel(%q)", strings.ToUpper(v))
		case "impactlevel", "impact":
			return fmt.Sprintf("impactLevel(%q)", strings.ToUpper(v))
		case "managementzone", "management_zone", "zone":
			return fmt.Sprintf("managementZones(%q)", v)
		}
		return fmt.Sprintf("entityTags(%q)", k+":"+v)
	}
	// A bare word is most useful as a title text match.
	return fmt.Sprintf("text(%q)", l)
}

// Poll returns the currently open problems as SourceItems. The since parameter
// is ignored on purpose: an ongoing problem must be returned on every poll so
// the dispatcher's active-instance / once dedup keeps shadowing it; the
// problem ID is unique per problem occurrence, so re-dispatch is impossible
// while the problem stays open.
func (a *Adapter) Poll(ctx context.Context, _ time.Time) ([]model.SourceItem, error) {
	selector := strings.Join(append([]string{`status("open")`}, a.selector...), ",")
	problems, err := a.client.problems(ctx, selector, time.Now().Add(-a.lookback))
	if err != nil {
		return nil, fmt.Errorf("dynatrace: polling problems: %w", err)
	}

	now := time.Now()
	var eligible []problem
	for _, p := range problems {
		if p.ProblemID == "" {
			aplog.Debug("dynatrace: skipping problem without problemId (title=%q)", p.Title)
			continue
		}
		// Flap dampener: too-young problems are left for a later poll (not
		// marked seen), so open→resolved blips shorter than min_age never
		// become tasks.
		if age := now.Sub(startTime(p)); age < a.minAge {
			aplog.Debug("dynatrace: problem %s age %s < min_age %s — deferred", p.ProblemID, age.Round(time.Second), a.minAge)
			continue
		}
		eligible = append(eligible, p)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Oldest first so a storm drains deterministically.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].StartTime < eligible[j].StartTime })

	current := make(map[string]struct{}, len(eligible))
	var items []model.SourceItem
	newCount := 0
	dropped := 0
	for _, p := range eligible {
		current[p.ProblemID] = struct{}{}
		if _, ok := a.seen[p.ProblemID]; !ok {
			if a.maxNewPerPoll > 0 && newCount >= a.maxNewPerPoll {
				dropped++
				delete(current, p.ProblemID) // not surfaced: stays unseen for the next poll
				continue
			}
			newCount++
			a.seen[p.ProblemID] = struct{}{}
		}
		items = append(items, a.toSourceItem(p))
	}
	if dropped > 0 {
		aplog.Warn("dynatrace: storm cap — %d new problem(s) deferred to next poll (max_new_per_poll=%d)", dropped, a.maxNewPerPoll)
	}

	// Prune problems that resolved so the seen map cannot grow without bound.
	// A later occurrence of the same failure gets a fresh problemId and
	// dispatches again as a new item.
	for id := range a.seen {
		if _, ok := current[id]; !ok {
			delete(a.seen, id)
		}
	}

	return items, nil
}

func startTime(p problem) time.Time { return time.UnixMilli(p.StartTime) }

func (a *Adapter) toSourceItem(p problem) model.SourceItem {
	title := p.Title
	if title == "" {
		title = "problem " + p.DisplayID
	}

	// Problem attributes become routable "key:value" item labels (the router
	// lowercases both sides), e.g. trigger match `labels: [severity:availability]`.
	labels := []string{
		"severity:" + strings.ToLower(p.SeverityLevel),
		"impact:" + strings.ToLower(p.ImpactLevel),
	}
	for _, z := range p.ManagementZones {
		labels = append(labels, "zone:"+z.Name)
	}
	for _, t := range p.EntityTags {
		labels = append(labels, tagLabel(t))
	}
	sort.Strings(labels)

	return model.SourceItem{
		ID:          p.ProblemID,
		SourceID:    a.ID(),
		Number:      p.DisplayID,
		Title:       title,
		Description: describe(p),
		Labels:      labels,
		Type:        "problem",
		Priority:    strings.ToLower(p.SeverityLevel),
		State:       "open",
		URL:         a.problemURL(p),
		Metadata: map[string]any{
			"problemId":       p.ProblemID,
			"displayId":       p.DisplayID,
			"severityLevel":   p.SeverityLevel,
			"impactLevel":     p.ImpactLevel,
			"status":          p.Status,
			"startTime":       startTime(p).UTC().Format(time.RFC3339),
			"managementZones": zoneNames(p.ManagementZones),
			"entityTags":      tagStrings(p.EntityTags),
		},
		CreatedAt: startTime(p),
		UpdatedAt: startTime(p),
	}
}

// problemURL is the browser deep link to the problem details view.
func (a *Adapter) problemURL(p problem) string {
	return fmt.Sprintf("%s/#problems/problemdetails;pid=%s", a.client.baseURL, p.ProblemID)
}

// tagLabel renders an entity tag as a routable "key:value" label; key-only
// tags keep Dynatrace's own string representation.
func tagLabel(t entityTag) string {
	if t.Key != "" && t.Value != "" {
		return t.Key + ":" + t.Value
	}
	if t.StringRepresentation != "" {
		return t.StringRepresentation
	}
	return t.Key + t.Value
}

// describe renders the full problem payload as the task description, so the
// investigating agent receives severity, entities, and the deep link without
// needing Dynatrace access of its own.
func describe(p problem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dynatrace problem **%s** — severity `%s`, impact `%s`.\n", p.DisplayID, p.SeverityLevel, p.ImpactLevel)

	if p.RootCauseEntity != nil && p.RootCauseEntity.Name != "" {
		fmt.Fprintf(&b, "\n**Root cause entity**: `%s` (%s)\n", p.RootCauseEntity.Name, p.RootCauseEntity.EntityID.Type)
	}

	if len(p.AffectedEntities) > 0 {
		b.WriteString("\n**Affected entities**\n\n")
		for _, e := range p.AffectedEntities {
			fmt.Fprintf(&b, "- `%s` (%s)\n", e.Name, e.EntityID.Type)
		}
	}

	if len(p.ManagementZones) > 0 {
		b.WriteString("\n**Management zones**\n\n")
		for _, z := range p.ManagementZones {
			fmt.Fprintf(&b, "- `%s`\n", z.Name)
		}
	}

	if len(p.EntityTags) > 0 {
		b.WriteString("\n**Tags**\n\n")
		for _, t := range p.EntityTags {
			fmt.Fprintf(&b, "- `%s`\n", tagLabel(t))
		}
	}

	fmt.Fprintf(&b, "\nOpen since %s.", startTime(p).UTC().Format(time.RFC3339))
	return b.String()
}

func zoneNames(zones []namedRef) []string {
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, z.Name)
	}
	return names
}

func tagStrings(tags []entityTag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, tagLabel(t))
	}
	return out
}

// Acknowledge is a no-op: a problem has no assignable/in-progress state to set.
func (a *Adapter) Acknowledge(_ context.Context, cell model.SourceItem, action model.AckAction) error {
	aplog.Debug("dynatrace: acknowledge %s (%s) — no-op for problems", cell.LogLabel(), action)
	return nil
}

// WriteResult is a no-op: result write-back is not implemented for problems.
// The intended pattern is a workflow step that publishes findings to a ticket
// source (APIARY_PUBLISH / APIARY_SPAWN) instead. Posting a problem comment
// (problems.write) is a possible future opt-in.
func (a *Adapter) WriteResult(_ context.Context, cell model.SourceItem, result model.RunResult) error {
	aplog.Debug("dynatrace: write result for %s (success=%v) — no-op for problems", cell.LogLabel(), result.Success)
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
