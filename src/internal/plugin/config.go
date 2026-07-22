package plugin

import (
	"fmt"
	"time"
)

type InstanceConfig struct {
	ID      string         `yaml:"id" json:"id"`
	Enabled *bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Timeout string         `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Config  map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

func (c InstanceConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c InstanceConfig) TimeoutDuration() (time.Duration, error) {
	if c.Timeout == "" {
		return 10 * time.Second, nil
	}
	duration, err := time.ParseDuration(c.Timeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("timeout %q must be a positive Go duration", c.Timeout)
	}
	return duration, nil
}

func ValidateConfigured(registry *Registry, instances []InstanceConfig) []error {
	errs := ValidateInstanceBasics(instances)
	seen := map[string]bool{}
	for i, instance := range instances {
		prefix := fmt.Sprintf("plugins[%d]", i)
		if instance.ID == "" {
			continue
		}
		if seen[instance.ID] {
			continue
		}
		seen[instance.ID] = true
		if !instance.IsEnabled() {
			continue
		}
		installed, ok := registry.Get(instance.ID)
		if !ok {
			errs = append(errs, fmt.Errorf("%s %q: plugin is enabled but not installed in plugin_dirs; install it or set enabled: false", prefix, instance.ID))
			continue
		}
		if len(installed.Manifest.ConfigSchema) > 0 {
			if err := ValidateValue(installed.Manifest.ConfigSchema, instance.Config); err != nil {
				errs = append(errs, fmt.Errorf("%s %q config: %w", prefix, instance.ID, err))
			}
		}
	}
	return errs
}

func ValidateInstanceBasics(instances []InstanceConfig) []error {
	var errs []error
	seen := map[string]bool{}
	for i, instance := range instances {
		prefix := fmt.Sprintf("plugins[%d]", i)
		if instance.ID == "" {
			errs = append(errs, fmt.Errorf("%s: id is required", prefix))
			continue
		}
		if seen[instance.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id %q", prefix, instance.ID))
		}
		seen[instance.ID] = true
		if _, err := instance.TimeoutDuration(); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", prefix, instance.ID, err))
		}
	}
	return errs
}

func EnabledClients(registry *Registry, instances []InstanceConfig, capability Capability) ([]*Client, []error) {
	var clients []*Client
	var errs []error
	for i, instance := range instances {
		if !instance.IsEnabled() || instance.ID == "" {
			continue
		}
		installed, ok := registry.Get(instance.ID)
		if !ok {
			continue
		}
		if !installed.Manifest.HasCapability(capability) {
			continue
		}
		client, err := NewClient(installed, instance)
		if err != nil {
			errs = append(errs, fmt.Errorf("plugins[%d] %q: %w", i, instance.ID, err))
			continue
		}
		clients = append(clients, client)
	}
	return clients, errs
}
