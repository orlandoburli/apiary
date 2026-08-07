package prometheus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// severityRank orders the severity label so a group can report the worst
// severity among its members as its priority. Unknown values rank lowest.
var severityRank = map[string]int{
	"critical": 4,
	"error":    3,
	"warning":  2,
	"info":     1,
}

// groupKey is the stable identity of an Alertmanager group: the receiver it
// routes to plus its group-by label set. The v2 API does not return
// Alertmanager's internal group key, so it is reconstructed from the two
// things that define the group.
func groupKey(g alertGroup) string {
	parts := make([]string, 0, len(g.Labels))
	for _, k := range sortedKeys(g.Labels) {
		parts = append(parts, k+"="+g.Labels[k])
	}
	return g.Receiver.Name + "/{" + strings.Join(parts, ",") + "}"
}

// groupItemID is the per-fire-cycle identity of a group: its key plus the
// epoch pinned when the group became non-empty.
//
// A group has no startsAt of its own, and its membership churns constantly
// while an incident unfolds — so deriving the epoch from the current members on
// every poll would make the identity jump the moment the oldest alert resolves,
// re-dispatching an incident that is still being investigated. Pinning the
// epoch for the whole cycle absorbs that churn: the id changes only when the
// group empties and later fires again, which is exactly one fire cycle.
func groupItemID(key string, epoch time.Time) string {
	return key + ":" + epoch.UTC().Format(time.RFC3339)
}

// groupNumber is a short, stable human-facing reference for a group, mirroring
// the 7-char fingerprint prefix used for single alerts.
func groupNumber(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:7]
}

// epochFor returns the pinned cycle-start for a group, pinning it on first
// sight. Callers must hold a.mu.
//
// On a cold start (daemon restarted mid-incident) there is nothing pinned, so
// the epoch is seeded from the members' earliest startsAt — which reproduces
// the value the previous process pinned whenever the oldest member is still
// firing, so an ongoing investigation is not duplicated. It differs only in the
// narrow case where that oldest member resolved during the restart.
func (a *Adapter) epochFor(key string, members []alert) time.Time {
	if epoch, ok := a.groupEpoch[key]; ok {
		return epoch
	}
	epoch := earliestStart(members)
	a.groupEpoch[key] = epoch
	aplog.Debug("prometheus: group %s cycle started at %s (%d alert(s))", key, epoch.UTC().Format(time.RFC3339), len(members))
	return epoch
}

func earliestStart(members []alert) time.Time {
	var earliest time.Time
	for _, al := range members {
		if earliest.IsZero() || al.StartsAt.Before(earliest) {
			earliest = al.StartsAt
		}
	}
	return earliest
}

// pollGroups is the group-dispatch variant of Poll: one SourceItem per
// non-empty Alertmanager group instead of one per alert.
func (a *Adapter) pollGroups(ctx context.Context) ([]model.SourceItem, error) {
	groups, err := a.client.alertGroups(ctx, a.filters)
	if err != nil {
		return nil, fmt.Errorf("prometheus: polling alert groups: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	type candidate struct {
		group alertGroup
		key   string
		epoch time.Time
	}

	var eligible []candidate
	live := map[string]struct{}{}
	for _, g := range groups {
		members := firingMembers(g)
		if len(members) == 0 {
			continue // an empty group is not a fire cycle
		}
		key := groupKey(g)
		live[key] = struct{}{}
		epoch := a.epochFor(key, members)

		// Flap dampener, applied to the cycle rather than to each alert: a
		// group that has only just formed is left for a later poll. The epoch
		// stays pinned meanwhile, so maturing does not change the identity.
		if age := now.Sub(epoch); age < a.minAge {
			aplog.Debug("prometheus: group %s age %s < min_age %s — deferred", key, age.Round(time.Second), a.minAge)
			continue
		}
		g.alerts = members
		eligible = append(eligible, candidate{group: g, key: key, epoch: epoch})
	}

	// Oldest cycle first so a storm drains deterministically.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].epoch.Before(eligible[j].epoch) })

	current := make(map[string]struct{}, len(eligible))
	var items []model.SourceItem
	newCount, dropped := 0, 0
	for _, c := range eligible {
		id := groupItemID(c.key, c.epoch)
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
		item := a.toGroupItem(c.group, c.key, c.epoch)
		a.labels[id] = groupSilenceLabels(c.group)
		items = append(items, item)
	}
	if dropped > 0 {
		aplog.Warn("prometheus: storm cap — %d new group(s) deferred to next poll (max_new_per_poll=%d)", dropped, a.maxNewPerPoll)
	}

	for id := range a.seen {
		if _, ok := current[id]; !ok {
			delete(a.seen, id)
			delete(a.labels, id)
		}
	}

	// A group that emptied ends its cycle: drop the pin so the next time it
	// fires it is a new item. Groups merely held back by the storm cap or
	// min_age are still live and keep theirs.
	for key := range a.groupEpoch {
		if _, ok := live[key]; !ok {
			delete(a.groupEpoch, key)
		}
	}

	return items, nil
}

// firingMembers drops members that have already resolved but still linger in
// the group until Alertmanager's resolve_timeout.
func firingMembers(g alertGroup) []alert {
	now := time.Now()
	members := make([]alert, 0, len(g.Alerts))
	for _, al := range g.Alerts {
		if al.Fingerprint == "" {
			continue
		}
		if !al.EndsAt.IsZero() && al.EndsAt.Before(now) {
			continue
		}
		members = append(members, al)
	}
	return members
}

// groupSilenceLabels are the labels true of the whole group: its group-by
// labels, plus any label every member carries with the same value. Silencing on
// them suppresses exactly this group's alerts, and they are what trigger
// matching sees — so `labels: [severity:critical]` matches a group whose
// members are all critical, even when severity is not a group-by label.
func groupSilenceLabels(g alertGroup) map[string]string {
	labels := map[string]string{}
	for k, v := range g.Labels {
		labels[k] = v
	}
	if len(g.alerts) == 0 {
		return labels
	}

	for k, v := range g.alerts[0].Labels {
		if _, ok := labels[k]; ok {
			continue
		}
		shared := true
		for _, al := range g.alerts[1:] {
			if al.Labels[k] != v {
				shared = false
				break
			}
		}
		if shared {
			labels[k] = v
		}
	}
	return labels
}

func (a *Adapter) toGroupItem(g alertGroup, key string, epoch time.Time) model.SourceItem {
	labels := groupSilenceLabels(g)

	name := g.Labels["alertname"]
	if name == "" {
		name = labels["alertname"]
	}
	if name == "" {
		name = "alert group"
	}
	title := fmt.Sprintf("%s (%d alerts)", name, len(g.alerts))
	if len(g.alerts) == 1 {
		title = fmt.Sprintf("%s (1 alert)", name)
	}

	itemLabels := make([]string, 0, len(labels))
	for k, v := range labels {
		itemLabels = append(itemLabels, k+":"+v)
	}
	sort.Strings(itemLabels)

	fingerprints := make([]string, 0, len(g.alerts))
	for _, al := range g.alerts {
		fingerprints = append(fingerprints, al.Fingerprint)
	}

	return model.SourceItem{
		ID:          groupItemID(key, epoch),
		SourceID:    a.ID(),
		Number:      groupNumber(key),
		Title:       title,
		Description: describeGroup(g, key, epoch),
		Labels:      itemLabels,
		Type:        "alert_group",
		Priority:    worstSeverity(g.alerts),
		State:       "firing",
		URL:         firstGeneratorURL(g.alerts),
		Metadata: map[string]any{
			"groupKey":     key,
			"epoch":        epoch.UTC().Format(time.RFC3339),
			"receiver":     g.Receiver.Name,
			"groupLabels":  g.Labels,
			"labels":       labels,
			"alertCount":   len(g.alerts),
			"fingerprints": fingerprints,
		},
	}
}

func worstSeverity(members []alert) string {
	worst, rank := "", 0
	for _, al := range members {
		s := al.Labels["severity"]
		if r := severityRank[strings.ToLower(s)]; r > rank {
			worst, rank = s, r
		}
	}
	return worst
}

func firstGeneratorURL(members []alert) string {
	for _, al := range members {
		if al.GeneratorURL != "" {
			return al.GeneratorURL
		}
	}
	return ""
}

// describeGroup renders the group as the task body: what the group is, then
// each member alert, so the agent sees the whole incident in one place.
func describeGroup(g alertGroup, key string, epoch time.Time) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Alertmanager group with %d firing alert(s).\n\n", len(g.alerts))
	fmt.Fprintf(&b, "- Group key: `%s`\n", key)
	fmt.Fprintf(&b, "- Receiver: `%s`\n", g.Receiver.Name)
	fmt.Fprintf(&b, "- Firing since %s\n", epoch.UTC().Format(time.RFC3339))

	if len(g.Labels) > 0 {
		b.WriteString("\n**Group labels**\n\n")
		for _, k := range sortedKeys(g.Labels) {
			fmt.Fprintf(&b, "- `%s`: %s\n", k, g.Labels[k])
		}
	}

	for i, al := range g.alerts {
		fmt.Fprintf(&b, "\n---\n\n### %d. %s\n\n", i+1, alertName(al))
		b.WriteString(describe(al))
		b.WriteString("\n")
	}
	return b.String()
}

func alertName(al alert) string {
	if n := al.Labels["alertname"]; n != "" {
		return n
	}
	return "alert " + al.Fingerprint
}

// groupLiveIDs computes the item IDs of every currently live group, for the
// resolution check. Callers must hold a.mu.
func (a *Adapter) groupLiveIDs(groups []alertGroup) map[string]struct{} {
	live := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		members := firingMembers(g)
		if len(members) == 0 {
			continue
		}
		key := groupKey(g)
		live[groupItemID(key, a.epochFor(key, members))] = struct{}{}
	}
	return live
}
