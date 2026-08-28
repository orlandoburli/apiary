package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/plugin"
	"github.com/orlandoburli/apiary/internal/version"
	"github.com/spf13/cobra"
)

// registryFlags are shared by every registry-backed subcommand.
type registryFlags struct {
	registry string
	offline  bool
}

func (f *registryFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.registry, "registry", "", "registry index URL to use for this command (https:// or file://)")
	cmd.Flags().BoolVar(&f.offline, "offline", false, "use the cached index and never touch the network")
}

// sources resolves which registries to consult: the flag wins, then
// apiary.yaml, then the official index. A missing or unreadable config is not
// fatal — search and info are useful outside a project directory, where there is
// no config to read.
//
// A URL passed with --registry still picks up the signing key configured for
// that same URL, so pointing at a mirror explicitly cannot quietly drop the
// verification the config asked for.
func (f *registryFlags) sources() ([]plugin.RegistrySource, error) {
	cfg, cfgErr := config.Load(configFile)
	if f.registry != "" {
		source := plugin.RegistrySource{URL: f.registry}
		if cfgErr == nil {
			for _, configured := range cfg.Registries() {
				if configured.URL == f.registry {
					source = configured
					break
				}
			}
		}
		if err := source.Validate(); err != nil {
			return nil, err
		}
		return []plugin.RegistrySource{source}, nil
	}
	if cfgErr != nil {
		return plugin.DefaultRegistrySources(), nil
	}
	sources := cfg.Registries()
	if len(sources) == 0 {
		return nil, errors.New("plugin_registries is empty: the registry is disabled for this project; install plugins manually or list a registry URL")
	}
	for i, source := range sources {
		if err := source.Validate(); err != nil {
			return nil, fmt.Errorf("plugin_registries[%d]: %w", i, err)
		}
	}
	return sources, nil
}

// load fetches every configured registry. Registries are consulted in order and
// the first hit wins, so a mirror can shadow the official index deliberately.
func (f *registryFlags) load(ctx context.Context, out io.Writer) ([]*plugin.LoadedIndex, error) {
	sources, err := f.sources()
	if err != nil {
		return nil, err
	}
	indexes := make([]*plugin.LoadedIndex, 0, len(sources))
	var failures []error
	for _, source := range sources {
		loaded, err := plugin.LoadIndex(ctx, source.URL, plugin.FetchOptions{Offline: f.offline, PublicKey: source.Key()})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if loaded.Warning != nil {
			fmt.Fprintf(out, "! %s is unreachable (%v); using the cached copy\n", source.URL, loaded.Warning)
		}
		if loaded.Unsigned {
			// Said once, plainly: an unverified index is a real state, and the
			// alternative — silence — would let it read as verified.
			fmt.Fprintf(out, "! %s is not signature-verified (no public key pinned for it)\n", source.URL)
		}
		indexes = append(indexes, loaded)
	}
	if len(indexes) == 0 {
		return nil, errors.Join(failures...)
	}
	for _, err := range failures {
		fmt.Fprintf(out, "! %v\n", err)
	}
	return indexes, nil
}

// find returns the first registry entry for an id, with the index it came from.
func (f *registryFlags) find(ctx context.Context, out io.Writer, id string) (*plugin.RegistryPlugin, *plugin.LoadedIndex, error) {
	indexes, err := f.load(ctx, out)
	if err != nil {
		return nil, nil, err
	}
	for _, loaded := range indexes {
		if entry, ok := loaded.Find(id); ok {
			return entry, loaded, nil
		}
	}
	var suggestions []string
	for _, loaded := range indexes {
		suggestions = append(suggestions, loaded.Suggest(id)...)
	}
	if len(suggestions) > 0 {
		return nil, nil, fmt.Errorf("no plugin %q in the registry; did you mean: %s", id, strings.Join(suggestions, ", "))
	}
	return nil, nil, fmt.Errorf("no plugin %q in the registry", id)
}

func newPluginsSearchCmd() *cobra.Command {
	flags := &registryFlags{}
	var capability string
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search the plugin registry",
		Long: "Search the plugin registry.\n\n" +
			"A registry listing is a pointer to someone else's repository. Nothing here is\n" +
			"downloaded, verified, or endorsed by the daemon — read the plugin's code before\n" +
			"you run it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			if capability != "" && !isKnownCapability(capability) {
				return fmt.Errorf("unknown capability %q; supported: %s", capability, strings.Join(plugin.SupportedCapabilityNames(), ", "))
			}
			out := cmd.OutOrStdout()
			indexes, err := flags.load(cmd.Context(), out)
			if err != nil {
				return err
			}
			writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			seen := map[string]bool{}
			matches := 0
			for _, loaded := range indexes {
				for _, entry := range loaded.Search(query, plugin.Capability(capability)) {
					if seen[entry.ID] {
						continue
					}
					seen[entry.ID] = true
					matches++
					latest := latestVersionFor(entry)
					fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", entry.ID, latest, capabilityList(entry.Capabilities), entry.Summary)
				}
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			if matches == 0 {
				fmt.Fprintln(out, "no plugins matched")
				return nil
			}
			fmt.Fprintf(out, "\n%d plugin(s). `apiary plugins info <id>` for details; listings are pointers, not endorsements.\n", matches)
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&capability, "capability", "", "only show plugins declaring this capability")
	return cmd
}

func newPluginsInfoCmd() *cobra.Command {
	flags := &registryFlags{}
	cmd := &cobra.Command{
		Use:   "info <id>[@version]",
		Short: "Show a registry listing and whether it can be installed here",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, requested := splitVersionSuffix(args[0])
			out := cmd.OutOrStdout()
			entry, loaded, err := flags.find(cmd.Context(), out, id)
			if err != nil {
				return err
			}
			printRegistryEntry(out, entry, loaded, requested)
			return nil
		},
	}
	flags.bind(cmd)
	return cmd
}

func printRegistryEntry(out io.Writer, entry *plugin.RegistryPlugin, loaded *plugin.LoadedIndex, requested string) {
	fmt.Fprintf(out, "%s\n", entry.ID)
	fmt.Fprintf(out, "  %s\n\n", entry.Summary)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(writer, "  capabilities\t%s\n", capabilityList(entry.Capabilities))
	fmt.Fprintf(writer, "  repository\t%s\n", entry.Repository)
	if entry.Homepage != "" {
		fmt.Fprintf(writer, "  homepage\t%s\n", entry.Homepage)
	}
	if entry.License != "" {
		fmt.Fprintf(writer, "  license\t%s\n", entry.License)
	}
	fmt.Fprintf(writer, "  registry\t%s%s%s\n", loaded.URL, cacheNote(loaded), signatureNote(loaded))
	_ = writer.Flush()

	// Resolution answers the only question that matters before a download: can
	// this host actually run any of these releases?
	resolved, resolveErr := entry.Resolve(plugin.ResolveOptions{Version: requested, ApiaryVersion: version.Version})
	fmt.Fprintf(out, "\n  releases\n")
	releases := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i := range entry.Releases {
		release := &entry.Releases[i]
		marker := "  "
		if resolved != nil && resolved.Release == release {
			marker = "→ "
		}
		state := releaseState(release)
		fmt.Fprintf(releases, "    %s%s\t%s\t%s\t%s\t%s\n", marker, release.Version, "apiary "+release.Apiary,
			"protocol "+fmt.Sprint(release.Protocol), state, strings.Join(release.Platforms(), " "))
	}
	_ = releases.Flush()

	fmt.Fprintln(out)
	defer func() {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  A listing is a pointer to someone else's repository — it is not an endorsement,")
		fmt.Fprintln(out, "  and a plugin runs with the daemon's OS permissions, as its user. Read the code.")
	}()
	if resolveErr != nil {
		fmt.Fprintf(out, "  Not installable on this host (%s/%s, apiary %s):\n    %v\n", runtime.GOOS, runtime.GOARCH, version.Version, resolveErr)
		return
	}
	if resolved.CompatibilityUnchecked != "" {
		fmt.Fprintf(out, "  ! this build reports version %q, which is not semantic versioning — the release's\n", resolved.CompatibilityUnchecked)
		fmt.Fprintf(out, "    apiary constraint (%s) could not be checked against it\n\n", resolved.Release.Apiary)
	}
	fmt.Fprintf(out, "  Installable here: %s %s for %s\n", entry.ID, resolved.Release.Version, resolved.Artifact.Platform())
	fmt.Fprintf(out, "    from    %s\n", resolved.Artifact.URL)
	fmt.Fprintf(out, "    sha256  %s\n", shortDigest(resolved.Artifact.ArchiveSHA256))
}

// releaseState describes a release the way an operator reads the list: what CI
// observed, and whether the publisher has withdrawn it.
func releaseState(release *plugin.Release) string {
	if release.Yanked {
		return "yanked: " + release.YankedReason
	}
	if release.Conformance == nil {
		return "conformance not run"
	}
	switch release.Conformance.Status {
	case "passed":
		return "conformance passed"
	case "failed":
		return "conformance FAILED"
	default:
		return "conformance not run"
	}
}

// signatureNote states what the index's provenance actually is, wherever the
// registry is named.
func signatureNote(loaded *plugin.LoadedIndex) string {
	if loaded.Unsigned {
		return " (unverified)"
	}
	return " (signature verified)"
}

func cacheNote(loaded *plugin.LoadedIndex) string {
	if loaded.FromCache {
		return " (cached)"
	}
	return ""
}

func capabilityList(capabilities []plugin.Capability) string {
	names := make([]string, len(capabilities))
	for i, capability := range capabilities {
		names[i] = string(capability)
	}
	return strings.Join(names, ",")
}

func latestVersionFor(entry *plugin.RegistryPlugin) string {
	resolved, err := entry.Resolve(plugin.ResolveOptions{ApiaryVersion: version.Version})
	if err != nil {
		return "-"
	}
	return resolved.Release.Version
}

func isKnownCapability(name string) bool {
	for _, known := range plugin.SupportedCapabilityNames() {
		if known == name {
			return true
		}
	}
	return false
}

// splitVersionSuffix accepts "id" and "id@version". Plugin ids never contain
// "@", so the split is unambiguous.
func splitVersionSuffix(argument string) (id, requestedVersion string) {
	if at := strings.LastIndex(argument, "@"); at > 0 {
		return argument[:at], argument[at+1:]
	}
	return argument, ""
}

func shortDigest(digest string) string {
	digest = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if len(digest) <= 16 {
		return digest
	}
	return digest[:8] + "…" + digest[len(digest)-8:]
}
