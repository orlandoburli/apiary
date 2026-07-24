package cli

import (
	"fmt"
	"sort"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var connectivity bool
	var env string
	var allEnvs bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate apiary.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			if allEnvs {
				return validateAllEnvironments(cmd, cfg)
			}
			if env != "" {
				return validateEnvironment(cmd, cfg, env)
			}
			return validateConfig(cmd, cfg, "base config")
		},
	}

	cmd.Flags().BoolVar(&connectivity, "connectivity", false, "also test source connectivity")
	cmd.Flags().StringVar(&env, "env", "", "validate a named environment overlay")
	cmd.Flags().BoolVar(&allEnvs, "all-envs", false, "validate the base config and all declared environments")
	return cmd
}

func validateConfig(cmd *cobra.Command, cfg *config.Config, label string) error {
	errs := cfg.Validate()
	if len(errs) == 0 {
		_, pluginErrs := configuredPlugins(cfg)
		errs = append(errs, pluginErrs...)
	}
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [%s] %s\n", label, e)
		}
		return fmt.Errorf("%s: %d validation error(s)", label, len(errs))
	}
	for _, w := range cfg.WorkflowWarnings() {
		fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠ [%s] %s\n", label, w)
	}
	fmt.Printf("✓ %s is valid\n", label)
	return nil
}

func validateEnvironment(cmd *cobra.Command, base *config.Config, name string) error {
	resolved, err := base.ForEnvironment(name)
	if err != nil {
		return err
	}
	return validateConfig(cmd, resolved, "environments."+name)
}

func validateAllEnvironments(cmd *cobra.Command, base *config.Config) error {
	var anyErr bool

	if err := validateConfig(cmd, base, "base config"); err != nil {
		anyErr = true
	}

	names := base.EnvironmentNames()
	sort.Strings(names)
	for _, name := range names {
		resolved, err := base.ForEnvironment(name)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [environments.%s] %s\n", name, err)
			anyErr = true
			continue
		}
		if err := validateConfig(cmd, resolved, "environments."+name); err != nil {
			anyErr = true
		}
	}

	if anyErr {
		return fmt.Errorf("one or more environments failed validation")
	}
	return nil
}
