package config

// ApplyProfile overlays a named runner profile onto the config's agents,
// overriding runner, model, fallbacks and fallback strategy for each agent the
// profile names. It returns how many agents were overridden and whether the
// profile existed at all, leaving the logging decision to the caller.
//
// NOTE: daemon.New carries an equivalent inline block. It is not collapsed onto
// this function here because dispatcher.New is a critical-risk symbol (7 direct
// callers across cli, daemon and plugin); that refactor belongs in its own PR.
func ApplyProfile(cfg *Config, name string) (applied int, found bool) {
	if name == "" || cfg == nil {
		return 0, true
	}
	profile, ok := cfg.Profiles[name]
	if !ok {
		return 0, false
	}
	for i := range cfg.Agents {
		ac := &cfg.Agents[i]
		overrides, ok := profile[ac.ID]
		if !ok {
			continue
		}
		if overrides.Runner != "" {
			ac.Runner = overrides.Runner
		}
		if overrides.Model != "" {
			ac.Model = overrides.Model
		}
		if overrides.Fallbacks != nil {
			ac.Fallbacks = overrides.Fallbacks
		}
		if overrides.FallbackStrategy != "" {
			ac.FallbackStrategy = overrides.FallbackStrategy
		}
		applied++
	}
	return applied, true
}
