package config

import "testing"

func TestValidateImprove(t *testing.T) {
	cases := []struct {
		name    string
		improve ImproveSettings
		agents  []AgentConfig
		wantErr bool
	}{
		{"empty is fine", ImproveSettings{}, nil, false},
		{"known agent", ImproveSettings{Agent: "improver"}, []AgentConfig{{ID: "improver"}}, false},
		{"unknown agent", ImproveSettings{Agent: "ghost"}, []AgentConfig{{ID: "improver"}}, true},
		{
			"valid effort keys",
			ImproveSettings{EffortModels: map[string]string{"quick": "haiku", "deep": "opus"}},
			nil, false,
		},
		{
			// A typo here would otherwise be silently ignored, leaving the operator
			// believing they pinned a cheap model while paying for the default.
			"typo in effort key",
			ImproveSettings{EffortModels: map[string]string{"stanadrd": "sonnet"}},
			nil, true,
		},
		{
			"empty model",
			ImproveSettings{EffortModels: map[string]string{"quick": ""}},
			nil, true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Agents: tc.agents}
			cfg.Settings.Improve = tc.improve
			errs := validateImprove(cfg)
			if got := len(errs) > 0; got != tc.wantErr {
				t.Errorf("validateImprove errors = %v, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}
