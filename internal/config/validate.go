package config

import "fmt"

// Validate checks the config for structural errors.
func (c *Config) Validate() []error {
	var errs []error

	if c.Version == "" {
		errs = append(errs, fmt.Errorf("version is required"))
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
		if r.Worker == "" {
			errs = append(errs, fmt.Errorf("routes[%d] %q: worker is required", i, r.ID))
		}
		if r.Worker != "" && !workerIDs[r.Worker] {
			errs = append(errs, fmt.Errorf("routes[%d] %q: worker %q not defined", i, r.ID, r.Worker))
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
