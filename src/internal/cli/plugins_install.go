package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/plugin"
	"github.com/orlandoburli/apiary/internal/version"
	"github.com/spf13/cobra"
)

// installFlags are the knobs shared by install and upgrade.
type installFlags struct {
	registry registryFlags
	dir      string
	yes      bool
	sha256   string
}

func (f *installFlags) bind(cmd *cobra.Command) {
	f.registry.bind(cmd)
	cmd.Flags().StringVar(&f.dir, "dir", "", "plugin directory to install into (default: the first plugin_dirs entry)")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "skip the confirmation prompt (the trust summary is still printed)")
	cmd.Flags().StringVar(&f.sha256, "sha256", "", "expected archive digest; cross-checked against the registry's before anything is downloaded")
}

// targetDir resolves where a plugin should be installed: --dir, else the first
// configured plugin directory — the same paths discovery reads, so an install
// can never land somewhere the daemon does not look.
func (f *installFlags) targetDir(cfg *config.Config) (string, error) {
	if f.dir != "" {
		return filepath.Abs(f.dir)
	}
	dirs := plugin.ConfiguredDirs(cfg.PluginDirs, configFile)
	if len(dirs) == 0 {
		return "", errors.New("no plugin directory configured; pass --dir or set plugin_dirs")
	}
	return dirs[0], nil
}

func newPluginsInstallCmd() *cobra.Command {
	flags := &installFlags{}
	cmd := &cobra.Command{
		Use:   "install <id>[@version]",
		Short: "Install a plugin from the registry",
		Long: "Install a plugin from the registry.\n\n" +
			"The artifact is verified against the digest the registry publishes before it is\n" +
			"unpacked, its manifest is validated, and its executable is pinned. Nothing is\n" +
			"executed, and nothing is enabled: a plugin runs only once you add it to\n" +
			"apiary.yaml and restart the daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(cmd, args[0], flags, false)
		},
	}
	flags.bind(cmd)
	return cmd
}

func newPluginsUpgradeCmd() *cobra.Command {
	flags := &installFlags{}
	var rollback bool
	cmd := &cobra.Command{
		Use:   "upgrade <id>[@version]",
		Short: "Upgrade an installed plugin to a newer registry release",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rollback {
				return runRollback(cmd, args[0], flags)
			}
			return runInstall(cmd, args[0], flags, true)
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&rollback, "rollback", false, "restore the previous version kept by the last upgrade")
	return cmd
}

func runInstall(cmd *cobra.Command, argument string, flags *installFlags, upgrade bool) error {
	id, requested := splitVersionSuffix(argument)
	out := cmd.OutOrStdout()

	cfg, err := config.Load(configFile)
	if err != nil {
		// Installing needs to know where plugins live, and that is a config
		// question. An empty config still answers it (the default directory).
		cfg = &config.Config{}
	}
	target, err := flags.targetDir(cfg)
	if err != nil {
		return err
	}
	existing, err := installedPlugin(cfg, id)
	if err != nil {
		return err
	}
	switch {
	case existing != nil && !upgrade:
		return fmt.Errorf("%s %s is already installed in %s; use `apiary plugins upgrade %s` to replace it",
			id, existing.Manifest.Version, existing.Root, id)
	case existing == nil && upgrade:
		return fmt.Errorf("%s is not installed; use `apiary plugins install %s`", id, id)
	case existing != nil:
		// Upgrading replaces the directory it is already installed in, wherever
		// that is — not wherever this invocation would have chosen.
		target = filepath.Dir(existing.Root)
	}

	entry, loaded, err := flags.registry.find(cmd.Context(), out, id)
	if err != nil {
		return err
	}
	resolved, err := entry.Resolve(plugin.ResolveOptions{Version: requested, ApiaryVersion: version.Version})
	if err != nil {
		return err
	}
	if existing != nil && existing.Manifest.Version == resolved.Release.Version {
		fmt.Fprintf(out, "%s %s is already the newest release the registry offers for this host.\n", id, existing.Manifest.Version)
		return nil
	}

	staged, err := plugin.Stage(cmd.Context(), resolved, version.Version, plugin.StageOptions{ExpectedArchiveSHA256: flags.sha256})
	if err != nil {
		return err
	}
	defer staged.Discard()

	printTrustSummary(out, staged, loaded, target, existing)
	if !flags.yes {
		confirmed, err := confirmTrust(cmd, fmt.Sprintf("Install into %s?", target))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "aborted; nothing was installed")
			return nil
		}
	}

	destination := filepath.Join(target, id)
	backup := destination + ".bak"
	if existing != nil {
		_ = os.RemoveAll(backup)
		if err := os.Rename(existing.Root, backup); err != nil {
			return fmt.Errorf("set the current version aside: %w", err)
		}
	}
	if err := staged.Commit(destination); err != nil {
		if existing != nil {
			_ = os.Rename(backup, existing.Root)
		}
		return err
	}
	// Prove the installed copy is what the daemon will accept, from the same
	// discovery path the daemon uses — and put the old version back if not.
	if _, err := plugin.Load(destination, version.Version); err != nil {
		_ = os.RemoveAll(destination)
		if existing != nil {
			_ = os.Rename(backup, existing.Root)
			return fmt.Errorf("the installed plugin does not validate (%w); the previous version was restored", err)
		}
		return fmt.Errorf("the installed plugin does not validate: %w", err)
	}

	printInstallOutcome(out, staged, destination, existing, backup)
	return nil
}

func runRollback(cmd *cobra.Command, argument string, flags *installFlags) error {
	id, _ := splitVersionSuffix(argument)
	out := cmd.OutOrStdout()
	cfg, err := config.Load(configFile)
	if err != nil {
		cfg = &config.Config{}
	}
	target, err := flags.targetDir(cfg)
	if err != nil {
		return err
	}
	if existing, err := installedPlugin(cfg, id); err == nil && existing != nil {
		target = filepath.Dir(existing.Root)
	}
	destination, backup := filepath.Join(target, id), filepath.Join(target, id+".bak")
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("no previous version kept for %s (looked for %s)", id, backup)
	}
	previous, err := plugin.Load(backup, version.Version)
	if err != nil {
		return fmt.Errorf("the kept version does not validate: %w", err)
	}
	swap := destination + ".rolling"
	_ = os.RemoveAll(swap)
	if err := os.Rename(destination, swap); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(backup, destination); err != nil {
		_ = os.Rename(swap, destination)
		return err
	}
	_ = os.RemoveAll(swap)
	fmt.Fprintf(out, "✓ restored %s %s in %s\n", id, previous.Manifest.Version, destination)
	fmt.Fprintln(out, "  A running daemon keeps the version it started with until you restart it.")
	return nil
}

func newPluginsUninstallCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "uninstall <id>",
		Short: "Remove an installed plugin directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			out := cmd.OutOrStdout()
			cfg, err := config.Load(configFile)
			if err != nil {
				cfg = &config.Config{}
			}
			installed, err := installedPlugin(cfg, id)
			if err != nil {
				return err
			}
			if installed == nil {
				return fmt.Errorf("%s is not installed in plugin_dirs", id)
			}
			for _, instance := range cfg.Plugins {
				if instance.ID == id && instance.IsEnabled() && !force {
					return fmt.Errorf("%s is enabled in %s; remove its plugins: entry (or set enabled: false) first, or pass --force", id, configFile)
				}
			}
			if err := os.RemoveAll(installed.Root); err != nil {
				return err
			}
			_ = os.RemoveAll(installed.Root + ".bak")
			fmt.Fprintf(out, "✓ removed %s %s from %s\n", id, installed.Manifest.Version, installed.Root)
			fmt.Fprintf(out, "  Its plugins: entry in %s, if any, is left alone — apiary validate will point at it.\n", configFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove it even while it is enabled in apiary.yaml")
	return cmd
}

// installedPlugin finds one plugin across the configured directories. Discovery
// errors elsewhere (an unrelated broken plugin) must not block installing this
// one, so only this plugin's own state is reported.
func installedPlugin(cfg *config.Config, id string) (*plugin.Installed, error) {
	registry, _ := plugin.DiscoverConfigured(cfg.PluginDirs, configFile, version.Version)
	if installed, ok := registry.Get(id); ok {
		return installed, nil
	}
	return nil, nil
}

// printTrustSummary states, before anything is committed, exactly what is about
// to be placed on disk and what it will be able to do. --yes skips the prompt,
// never this.
func printTrustSummary(out io.Writer, staged *plugin.Staged, loaded *plugin.LoadedIndex, target string, existing *plugin.Installed) {
	manifest := staged.Installed.Manifest
	fmt.Fprintf(out, "\n%s %s  (%s)\n", manifest.ID, manifest.Version, capabilityList(manifest.Capabilities))
	if existing != nil {
		fmt.Fprintf(out, "  replaces %s, kept as %s until the next upgrade\n", existing.Manifest.Version, filepath.Base(existing.Root)+".bak")
	}
	fmt.Fprintf(out, "  from     %s\n", staged.Resolved.Artifact.URL)
	fmt.Fprintf(out, "  sha256   %s (verified)\n", shortDigest(staged.Resolved.Artifact.ArchiveSHA256))
	fmt.Fprintf(out, "  registry %s%s%s\n", loaded.URL, cacheNote(loaded), signatureNote(loaded))
	fmt.Fprintf(out, "  %s\n", conformanceLine(staged.Resolved.Release))
	if staged.PinInjected {
		fmt.Fprintf(out, "  pinned   %s (from the registry, not from the archive)\n", shortDigest(staged.Resolved.Artifact.ExecutableSHA256))
	} else {
		fmt.Fprintf(out, "  pinned   %s (by its publisher; matches the registry)\n", shortDigest(manifest.Checksum))
	}

	fmt.Fprintln(out, "\n  Declared access (a declaration, not a sandbox):")
	fmt.Fprintf(out, "    network      %s\n", yesNo(manifest.Security.Network))
	fmt.Fprintf(out, "    read paths   %s\n", pathList(manifest.Security.ReadPaths))
	fmt.Fprintf(out, "    write paths  %s\n", pathList(manifest.Security.WritePaths))
	fmt.Fprintf(out, "    secret env   %s\n", pathList(manifest.Security.SecretEnv))
	fmt.Fprintln(out, "\n  This executable will run with the daemon's OS permissions, as its user.")
	fmt.Fprintln(out, "  A registry listing is a pointer to someone else's repository — it is not an")
	fmt.Fprintln(out, "  endorsement, and Apiary has not reviewed this code.")
	fmt.Fprintln(out)
}

func printInstallOutcome(out io.Writer, staged *plugin.Staged, destination string, existing *plugin.Installed, backup string) {
	manifest := staged.Installed.Manifest
	verb := "installed"
	if existing != nil {
		verb = fmt.Sprintf("upgraded %s →", existing.Manifest.Version)
	}
	fmt.Fprintf(out, "✓ %s %s %s in %s\n", verb, manifest.ID, manifest.Version, destination)
	if existing != nil {
		fmt.Fprintf(out, "  previous version kept at %s (`--rollback` restores it)\n", backup)
		fmt.Fprintln(out, "\n  A running daemon keeps the version it started with until you restart it.")
		return
	}
	fmt.Fprintln(out, "\n  Nothing runs yet. Installing makes a plugin available; only a plugins: entry")
	fmt.Fprintf(out, "  in %s makes it run:\n\n", configFile)
	fmt.Fprint(out, instanceSnippet(manifest))
	fmt.Fprintln(out, "\n  Then run `apiary validate` and restart the daemon — plugin clients are created")
	fmt.Fprintln(out, "  at startup.")
}

// instanceSnippet renders a plugins: entry seeded with the config keys the
// manifest declares as required, so the operator edits values rather than
// guessing key names out of a JSON schema.
func instanceSnippet(manifest plugin.Manifest) string {
	var builder strings.Builder
	builder.WriteString("    plugins:\n")
	builder.WriteString(fmt.Sprintf("      - id: %s\n", manifest.ID))
	required := requiredConfigKeys(manifest.ConfigSchema)
	if len(required) == 0 {
		builder.WriteString("        config: {}\n")
		return builder.String()
	}
	builder.WriteString("        config:\n")
	for _, key := range required {
		builder.WriteString(fmt.Sprintf("          %s: %s\n", key.name, key.placeholder()))
	}
	return builder.String()
}

type schemaKey struct {
	name string
	kind string
}

func (k schemaKey) placeholder() string {
	switch k.kind {
	case "integer", "number":
		return "0"
	case "boolean":
		return "false"
	case "array":
		return "[]"
	case "object":
		return "{}"
	default:
		return "\"\""
	}
}

func requiredConfigKeys(raw json.RawMessage) []schemaKey {
	if len(raw) == 0 {
		return nil
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil
	}
	keys := make([]schemaKey, 0, len(schema.Required))
	for _, name := range schema.Required {
		keys = append(keys, schemaKey{name: name, kind: schema.Properties[name].Type})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].name < keys[j].name })
	return keys
}

func conformanceLine(release *plugin.Release) string {
	if release.Conformance == nil || release.Conformance.Status == "not_run" {
		return "conformance  not run against this release"
	}
	switch release.Conformance.Status {
	case "passed":
		return "conformance  passed the protocol kit in registry CI"
	default:
		return "conformance  FAILED the protocol kit in registry CI — expect protocol bugs"
	}
}

// confirmTrust asks once, on the command's own stdin. It exists alongside the
// package's simpler confirm() because a trust decision needs two things that one
// does not offer: a stream the tests can drive, and an explicit refusal when
// there is no terminal to answer — an unattended pipe must not be read as "no
// objection", it must be told to pass --yes.
func confirmTrust(cmd *cobra.Command, question string) (bool, error) {
	in := cmd.InOrStdin()
	if file, ok := in.(*os.File); ok {
		info, err := file.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice == 0 {
			return false, errors.New("nothing to read a confirmation from; re-run with --yes if you have read the summary above")
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func pathList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
