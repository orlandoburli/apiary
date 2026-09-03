package config

import "testing"

// TestIsTicketSourceType checks the structural signal behind the ticket-vs-
// routine split in the task list (issue #475): jira/github/plane are
// ticket trackers, while "plugin" (the routines/scheduler bridge) and the
// built-in monitoring adapters (dynatrace, prometheus) — which mint synthetic
// work items with no real ticket behind them, same as a routine occurrence —
// are not. This is an allow-list on purpose: a block-list on "plugin" alone
// would wrongly count dynatrace/prometheus alerts as ticket-bound, since they
// register under their own type, not "plugin".
func TestIsTicketSourceType(t *testing.T) {
	cases := map[string]bool{
		"jira":       true,
		"github":     true,
		"plane":      true,
		"plugin":     false,
		"dynatrace":  false,
		"prometheus": false,
		"":           false,
	}
	for typ, want := range cases {
		if got := IsTicketSourceType(typ); got != want {
			t.Errorf("IsTicketSourceType(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestConfigTicketSourceIDs(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{
			{ID: "jira", Type: "jira"},
			{ID: "github", Type: "github"},
			{ID: "routines", Type: "plugin"},
			{ID: "monitoring", Type: "plugin"},
		},
	}
	got := cfg.TicketSourceIDs()
	want := []string{"jira", "github"}
	if len(got) != len(want) {
		t.Fatalf("TicketSourceIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TicketSourceIDs() = %v, want %v", got, want)
		}
	}

	if got := (*Config)(nil).TicketSourceIDs(); got != nil {
		t.Fatalf("nil *Config: TicketSourceIDs() = %v, want nil", got)
	}

	empty := &Config{Sources: []SourceConfig{{ID: "routines", Type: "plugin"}}}
	if got := empty.TicketSourceIDs(); len(got) != 0 {
		t.Fatalf("no ticket sources configured: TicketSourceIDs() = %v, want empty", got)
	}
}
