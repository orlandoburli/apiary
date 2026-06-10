package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/creativeprojects/go-selfupdate"
	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/version"
)

// releaseSlug is the GitHub repository that hosts apiary releases.
const releaseSlug = "orlandoburli/apiary"

func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update apiary to the latest release",
		Long: "Checks GitHub releases for a newer version and replaces the current binary\n" +
			"in place after validating its checksum. Installs managed by a package\n" +
			"manager (Homebrew, Scoop) are redirected to the package manager instead.",
		Args: cobra.NoArgs,
		// A failed download/swap is a runtime error, not a usage error.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "check for a newer version without installing it")
	return cmd
}

func runUpdate(ctx context.Context, checkOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	updater, err := newUpdater()
	if err != nil {
		return err
	}
	latest, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(releaseSlug))
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}
	if !found || latest.LessOrEqual(version.Version) {
		fmt.Printf("apiary %s is up to date\n", version.Version)
		return nil
	}

	fmt.Printf("new version available: %s → %s\n", version.Version, latest.Version())

	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return fmt.Errorf("locating current binary: %w", err)
	}
	if hint := packageManagerHint(exe); hint != "" {
		fmt.Println(hint)
		return nil
	}
	if checkOnly {
		fmt.Println("run 'apiary update' to install it")
		return nil
	}

	fmt.Printf("downloading %s...\n", latest.AssetName)
	if err := updater.UpdateTo(ctx, latest, exe); err != nil {
		return fmt.Errorf("updating binary: %w", err)
	}
	fmt.Printf("✓ updated to %s (%s)\n", latest.Version(), exe)
	fmt.Println("if the apiary daemon is running, restart it to pick up the new version")
	return nil
}

// newUpdater builds an updater that resolves releases from GitHub and
// validates downloads against the GoReleaser checksums.txt asset.
// GITHUB_TOKEN is picked up from the environment automatically (optional —
// it only raises the API rate limit).
func newUpdater() (*selfupdate.Updater, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, fmt.Errorf("initializing GitHub release source: %w", err)
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:    source,
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return nil, fmt.Errorf("initializing updater: %w", err)
	}
	return updater, nil
}

// packageManagerHint reports how to upgrade when the binary location shows it
// is owned by a package manager, so self-updating would fight the manager's
// own bookkeeping. Returns "" when the install is safe to update in place.
// The path must have symlinks already resolved (Homebrew casks symlink the
// binary into bin/, but the real file lives under Caskroom).
func packageManagerHint(exePath string) string {
	// Normalize separators by hand: filepath.ToSlash only converts the
	// current platform's separator, and these path shapes are OS-specific.
	p := strings.ToLower(strings.ReplaceAll(exePath, `\`, "/"))
	switch {
	case strings.Contains(p, "/caskroom/"), strings.Contains(p, "/cellar/"):
		return "this install is managed by Homebrew — run 'brew upgrade --cask apiary' instead"
	case strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return "this install is managed by Homebrew — run 'brew upgrade --cask apiary' instead"
	case strings.Contains(p, "/scoop/"):
		return "this install is managed by Scoop — run 'scoop update apiary' instead"
	}
	return ""
}
