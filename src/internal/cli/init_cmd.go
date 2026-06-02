package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const starterConfig = `version: "1"

sources:
  - id: my-source
    type: plane
    config:
      workspace: my-workspace
      project: my-project
      api_key: ${PLANE_API_KEY}
    poll_interval: 60s
    filters:
      labels: [ai-ready]

workers:
  - id: default-worker
    description: "Default worker"
    runner: cli
    model: openai/gpt-4o
    config:
      command: opencode        # CLI binary to invoke (opencode, gemini, etc.)
      model_flag: "--model"    # flag used to pass the model to the CLI
      working_dir: .
      max_turns: 10

routes:
  - id: default
    priority: 99
    match:
      source: my-source
    worker: default-worker

settings:
  concurrency: 2
  log_level: info
  state_lock: true
  result_comment: true
`

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a new apiary.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat("apiary.yaml"); err == nil {
				return fmt.Errorf("apiary.yaml already exists")
			}
			if err := os.WriteFile("apiary.yaml", []byte(starterConfig), 0644); err != nil {
				return err
			}
			fmt.Println("✓ apiary.yaml created — edit it to configure your sources and workers")
			return nil
		},
	}
}
