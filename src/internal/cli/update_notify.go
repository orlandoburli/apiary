package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/creativeprojects/go-selfupdate"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/orlandoburli/apiary/internal/version"
)

// updateCheckInterval is how often the CLI checks GitHub for a newer release.
const updateCheckInterval = 24 * time.Hour

// updateCheckTimeout bounds the once-a-day network call so a slow or offline
// connection never stalls the CLI noticeably.
const updateCheckTimeout = 3 * time.Second

// updateCheckState is the on-disk cache of the last update check.
type updateCheckState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version"`
}

// maybeNotifyUpdate prints a stderr notice when a newer release is known to
// exist. At most once per updateCheckInterval it refreshes the cached latest
// version from GitHub. It never fails: any error just skips the notice.
//
// The check is skipped entirely for non-interactive sessions (scripts, cron,
// CI), for the daemon and update/version commands, and when
// APIARY_NO_UPDATE_CHECK is set.
func maybeNotifyUpdate(cmd *cobra.Command) {
	if os.Getenv("APIARY_NO_UPDATE_CHECK") != "" {
		return
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	switch topLevelCommand(cmd).Name() {
	// update/version already talk about versions; run/service are the daemon
	// (long-lived, logs to files); help/completion must stay machine-clean.
	case "update", "version", "run", "service", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return
	}

	path, err := updateCheckStatePath()
	if err != nil {
		return
	}
	state := loadUpdateCheckState(path)
	if time.Since(state.CheckedAt) >= updateCheckInterval {
		state = refreshUpdateCheckState(cmd.Context())
		saveUpdateCheckState(path, state)
	}
	if newerVersionAvailable(version.Version, state.Latest) {
		fmt.Fprintf(os.Stderr, "\nA new version of apiary is available: %s → %s\nRun 'apiary update' to install it.\n\n", version.Version, state.Latest)
	}
}

// topLevelCommand walks up from a (possibly nested) subcommand to the
// command directly under the root, e.g. `apiary service start` → service.
func topLevelCommand(cmd *cobra.Command) *cobra.Command {
	c := cmd
	for c.Parent() != nil && c.Parent() != c.Root() {
		c = c.Parent()
	}
	return c
}

// refreshUpdateCheckState queries GitHub for the latest release version.
// CheckedAt is stamped even on failure so an unreachable network is retried
// once per interval, not on every invocation.
func refreshUpdateCheckState(ctx context.Context) updateCheckState {
	if ctx == nil {
		ctx = context.Background()
	}
	state := updateCheckState{CheckedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	updater, err := newUpdater()
	if err != nil {
		return state
	}
	latest, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(releaseSlug))
	if err == nil && found {
		state.Latest = latest.Version()
	}
	return state
}

// newerVersionAvailable reports whether latest is a strictly newer semver
// than current. Unknown or unparsable versions never notify.
func newerVersionAvailable(current, latest string) bool {
	if latest == "" {
		return false
	}
	cur, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	lat, err := semver.NewVersion(latest)
	if err != nil {
		return false
	}
	return lat.GreaterThan(cur)
}

func updateCheckStatePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "apiary", "update-check.json"), nil
}

func loadUpdateCheckState(path string) updateCheckState {
	var state updateCheckState
	data, err := os.ReadFile(path)
	if err != nil {
		return updateCheckState{}
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return updateCheckState{}
	}
	return state
}

func saveUpdateCheckState(path string, state updateCheckState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Best-effort cache: ignore write errors (read-only HOME, etc.).
	_ = os.WriteFile(path, data, 0o644)
}
