package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const starterConfig = `version: "1"

# ── Runners ────────────────────────────────────────────────────────────────────
# Define how agents execute (CLI, API, custom gateway, etc.)
runners:
  # Claude CLI runner (local development, no API keys needed)
  - id: claude-cli
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]

# Default runner used by agents if not overridden
default_runner: claude-cli

# ── Sources ────────────────────────────────────────────────────────────────────
# Where tasks come from (github, plane, ...)
sources:
  - id: my-repo
    type: github
    config:
      repo: my-org/my-repo
      # GitHub personal access token. Required for private repos.
      api_key: ${GITHUB_TOKEN}
    poll_interval: 120s
    filters:
      states: [open]
      labels: [ai-ready]

# ── Agents ─────────────────────────────────────────────────────────────────────
agents:
  - id: engineer
    description: "Engineer — implements tasks following project conventions"
    runner: claude-cli
    model: claude-sonnet-4-6
    # soul_file: .apiary/souls/engineer.md   # optional agent persona file

# ── Workflows ──────────────────────────────────────────────────────────────────
# Task routing: each workflow has a trigger that matches source items.
workflows:
  - id: implement
    trigger:
      priority: 20            # lower = evaluated first
      match:
        source: my-repo
        labels: [ai-ready]
    steps:
      - id: run
        agent: engineer
    on_complete:
      add_labels: [ai-complete]

# ── Settings ───────────────────────────────────────────────────────────────────
settings:
  concurrency: 2
  log_level: info
  state_lock: true
  # Stop re-dispatching a (task, workflow) after this many consecutive failures.
  max_attempts: 3
`

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a new apiary.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat("apiary.yaml"); err == nil {
				return fmt.Errorf("apiary.yaml already exists")
			}
			if err := os.WriteFile("apiary.yaml", []byte(starterConfig), 0o600); err != nil {
				return err
			}
			fmt.Println("✓ apiary.yaml created — edit it to configure your sources and agents")
			return nil
		},
	}
}
