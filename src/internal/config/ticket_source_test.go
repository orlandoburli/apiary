package config

import "testing"

// TestIsTicketSourceType checks the structural signal behind the ticket-vs-
// routine split in the task list (issue #475): any type other than "plugin"
// (a plugin-bridged scheduler/monitoring source) counts as a ticket tracker,
// so a future ticket-tracker type needs no special-casing here, and a future
// plugin-sourced routine is excluded the same way as today's.
func TestIsTicketSourceType(t *testing.T) {
	cases := map[string]bool{
		"jira":   true,
		"github": true,
		"plane":  true,
		"plugin": false,
		"":       false,
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
