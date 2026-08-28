package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/plugin"
	"github.com/orlandoburli/apiary/internal/version"
	"github.com/spf13/cobra"
)

func configuredPlugins(cfg *config.Config) (*plugin.Registry, []error) {
	registry, errs := plugin.DiscoverConfigured(cfg.PluginDirs, configFile, version.Version)
	errs = append(errs, plugin.ValidateConfigured(registry, cfg.Plugins)...)
	// Discovery checks that a pin is well-formed; only re-hashing the executable
	// checks that it is still true. The daemon does this at client creation and
	// before every invocation — doing it here means an operator finds out from
	// `validate`, rather than from a plugin that stops working at 3am.
	for _, installed := range registry.List() {
		if err := installed.VerifyPin(); err != nil {
			errs = append(errs, err)
		}
	}
	return registry, errs
}

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Inspect installed out-of-process plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listPlugins(cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List discovered plugins",
		RunE:  func(cmd *cobra.Command, args []string) error { return listPlugins(cmd) },
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <id>",
		Short: "Print one installed plugin manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			registry, discoveryErrs := plugin.DiscoverConfigured(cfg.PluginDirs, configFile, version.Version)
			if len(discoveryErrs) > 0 {
				return errors.Join(discoveryErrs...)
			}
			installed, ok := registry.Get(args[0])
			if !ok {
				return fmt.Errorf("plugin %q is not installed in plugin_dirs", args[0])
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(installed)
		},
	})
	cmd.AddCommand(newPluginsSearchCmd())
	cmd.AddCommand(newPluginsInfoCmd())
	cmd.AddCommand(newPluginsInstallCmd())
	cmd.AddCommand(newPluginsUpgradeCmd())
	cmd.AddCommand(newPluginsUninstallCmd())
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate discovered manifests and enabled plugin configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			_, errs := configuredPlugins(cfg)
			if len(errs) > 0 {
				return errors.Join(errs...)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ plugins are valid")
			return nil
		},
	})
	return cmd
}

func listPlugins(cmd *cobra.Command) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}
	registry, discoveryErrs := plugin.DiscoverConfigured(cfg.PluginDirs, configFile, version.Version)
	if len(discoveryErrs) > 0 {
		return errors.Join(discoveryErrs...)
	}
	enabled := map[string]bool{}
	for _, instance := range cfg.Plugins {
		enabled[instance.ID] = instance.IsEnabled()
	}
	for _, installed := range registry.List() {
		state := "installed"
		if active, configured := enabled[installed.Manifest.ID]; configured {
			if active {
				state = "enabled"
			} else {
				state = "disabled"
			}
		}
		capabilities := make([]string, len(installed.Manifest.Capabilities))
		for i, capability := range installed.Manifest.Capabilities {
			capabilities[i] = string(capability)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", installed.Manifest.ID, installed.Manifest.Version, state, strings.Join(capabilities, ","))
	}
	return nil
}
