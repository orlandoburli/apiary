package config

import (
	"fmt"
	"os"
)

// Validate checks the config for structural errors.
func (c *Config) Validate() []error {
	var errs []error

	if c.Version == "" {
		errs = append(errs, fmt.Errorf("version is required"))
	}

	runnerIDs := map[string]bool{}
	for i, r := range c.Runners {
		if r.ID == "" {
			errs = append(errs, fmt.Errorf("runners[%d]: id is required", i))
		}
		if r.Type == "" {
			errs = append(errs, fmt.Errorf("runners[%d] %q: type is required", i, r.ID))
		}
		if runnerIDs[r.ID] {
			errs = append(errs, fmt.Errorf("runners[%d]: duplicate id %q", i, r.ID))
		}
		runnerIDs[r.ID] = true
	}

	if c.DefaultRunner != "" && !runnerIDs[c.DefaultRunner] {
		errs = append(errs, fmt.Errorf("default_runner %q: not defined in runners", c.DefaultRunner))
	}

	sourceIDs := map[string]bool{}
	for i, s := range c.Sources {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: id is required", i))
		}
		if s.Type == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: type is required", i))
		}
		if sourceIDs[s.ID] {
			errs = append(errs, fmt.Errorf("sources[%d]: duplicate id %q", i, s.ID))
		}
		sourceIDs[s.ID] = true
	}

	agentIDs := map[string]bool{}
	for i, a := range c.Agents {
		if a.ID == "" {
			errs = append(errs, fmt.Errorf("agents[%d]: id is required", i))
		}
		if len(a.PreferredModels) == 0 {
			errs = append(errs, fmt.Errorf("agents[%d] %q: preferred_models is required and must not be empty", i, a.ID))
		}
		if a.SoulFile != "" {
			if _, err := os.Stat(a.SoulFile); err != nil {
				errs = append(errs, fmt.Errorf("agents[%d] %q: soul_file %q not found or not readable: %w", i, a.ID, a.SoulFile, err))
			}
		}
		if a.Runner != "" && !runnerIDs[a.Runner] {
			errs = append(errs, fmt.Errorf("agents[%d] %q: runner %q not defined", i, a.ID, a.Runner))
		}
		if agentIDs[a.ID] {
			errs = append(errs, fmt.Errorf("agents[%d]: duplicate id %q", i, a.ID))
		}
		agentIDs[a.ID] = true
	}

	workerIDs := map[string]bool{}
	for i, w := range c.Workers {
		if w.ID == "" {
			errs = append(errs, fmt.Errorf("workers[%d]: id is required", i))
		}
		if w.Runner == "" {
			errs = append(errs, fmt.Errorf("workers[%d] %q: runner is required", i, w.ID))
		}
		if w.Model == "" {
			errs = append(errs, fmt.Errorf("workers[%d] %q: model is required", i, w.ID))
		}
		if workerIDs[w.ID] {
			errs = append(errs, fmt.Errorf("workers[%d]: duplicate id %q", i, w.ID))
		}
		workerIDs[w.ID] = true
	}

	routeIDs := map[string]bool{}
	for i, r := range c.Routes {
		if r.ID == "" {
			errs = append(errs, fmt.Errorf("routes[%d]: id is required", i))
		}
		if r.Agent == "" {
			errs = append(errs, fmt.Errorf("routes[%d] %q: agent is required", i, r.ID))
		}
		if r.Agent != "" && !agentIDs[r.Agent] {
			errs = append(errs, fmt.Errorf("routes[%d] %q: agent %q not defined", i, r.ID, r.Agent))
		}
		if r.Match.Source != "" && !sourceIDs[r.Match.Source] {
			errs = append(errs, fmt.Errorf("routes[%d] %q: source %q not defined", i, r.ID, r.Match.Source))
		}
		if routeIDs[r.ID] {
			errs = append(errs, fmt.Errorf("routes[%d]: duplicate id %q", i, r.ID))
		}
		routeIDs[r.ID] = true
	}

	return errs
}
