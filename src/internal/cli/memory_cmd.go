package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/memory"
	"github.com/orlandoburli/apiary/internal/model"
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and curate the persistent agent memory",
		Long: "Inspect and curate the persistent agent memory store (settings.memory): the\n" +
			"global tier (durable facts written via APIARY_MEMORIZE) and the task tier\n" +
			"(per-task working notes). Entries are plain markdown — they can also be\n" +
			"edited or deleted by hand; the MEMORY.md index self-heals on the next write.",
	}
	cmd.AddCommand(
		newMemoryPathCmd(),
		newMemoryListCmd(),
		newMemoryShowCmd(),
		newMemoryRmCmd(),
		newMemoryPruneCmd(),
	)
	return cmd
}

// memoryRoot resolves the memory root the same way the daemon does:
// settings.memory.path when set, else <data-dir>/memory.
func memoryRoot() string {
	if cfg, err := config.Load(configFile); err == nil && cfg.Settings.Memory.Path != "" {
		return cfg.Settings.Memory.Path
	}
	return filepath.Join(config.DataDir(configFile), "memory")
}

// openMemoryStore opens the store at the resolved root. Open also rebuilds the
// index, healing any drift from hand edits.
func openMemoryStore() (*memory.Store, error) {
	return memory.Open(memoryRoot())
}

func newMemoryPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the memory root directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(memoryRoot())
			return nil
		},
	}
}

func newMemoryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List global memory entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openMemoryStore()
			if err != nil {
				return err
			}
			metas, err := store.List()
			if err != nil {
				return err
			}
			notes, _ := store.ListTaskNotes()
			if len(metas) == 0 {
				fmt.Println(instMuted.Render("No global memory entries."))
			}
			for _, m := range metas {
				line := fmt.Sprintf("%-32s %s", m.Name, m.Description)
				meta := ""
				if m.Agent != "" {
					meta = " (" + m.Agent
					if !m.Updated.IsZero() {
						meta += ", " + m.Updated.Local().Format("2006-01-02")
					}
					meta += ")"
				} else if !m.Updated.IsZero() {
					meta = " (" + m.Updated.Local().Format("2006-01-02") + ")"
				}
				fmt.Println(line + instMuted.Render(meta))
			}
			fmt.Println(instMuted.Render(fmt.Sprintf("%d global entr(ies), %d task note file(s) at %s", len(metas), len(notes), store.Root())))
			return nil
		},
	}
}

func newMemoryShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print one global memory entry (frontmatter + body)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openMemoryStore()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(store.Root(), "global", args[0]+".md"))
			if err != nil {
				return fmt.Errorf("entry %q not found", args[0])
			}
			fmt.Print(string(data))
			return nil
		},
	}
}

func newMemoryRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete one global memory entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openMemoryStore()
			if err != nil {
				return err
			}
			if err := store.Delete(args[0]); err != nil {
				return err
			}
			fmt.Println(instOK.Render("✓ Deleted ") + args[0])
			return nil
		},
	}
}

func newMemoryPruneCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune task notes past the retention window",
		Long: "Delete task-memory note files whose task has been terminal (done/failed)\n" +
			"longer than settings.memory.task_retention (default 720h). Notes whose task\n" +
			"is unknown (e.g. after a database reset) are pruned by file age instead.\n" +
			"Global entries are never pruned by this command — use rm.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryPrune(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pruned without deleting")
	return cmd
}

func runMemoryPrune(dryRun bool) error {
	store, err := openMemoryStore()
	if err != nil {
		return err
	}
	retention := 720 * time.Hour
	if cfg, err := config.Load(configFile); err == nil {
		retention = cfg.Settings.Memory.TaskRetentionDuration()
	}

	// Task states come from the project DB when it exists; without one, fall
	// back to file age for every note file.
	var dbClient *db.Client
	ctx := context.Background()
	if _, err := os.Stat(getDBPath()); err == nil {
		if c, err := db.New(ctx, getDBPath()); err == nil {
			dbClient = c
			defer dbClient.Close()
		}
	}

	notes, err := store.ListTaskNotes()
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-retention)
	pruned := 0
	for _, tn := range notes {
		prune := false
		if dbClient != nil {
			if task, err := dbClient.InternalTasks().GetTask(ctx, tn.TaskID); err == nil && task != nil {
				terminal := task.State == model.TaskStateDone || task.State == model.TaskStateFailed
				prune = terminal && task.UpdatedAt.Before(cutoff)
			} else {
				prune = tn.ModTime.Before(cutoff)
			}
		} else {
			prune = tn.ModTime.Before(cutoff)
		}
		if !prune {
			continue
		}
		if dryRun {
			fmt.Println(instMuted.Render("would prune ") + tn.TaskID)
			pruned++
			continue
		}
		if err := store.DeleteTaskNotes(tn.TaskID); err != nil {
			fmt.Println(instErr.Render("prune "+tn.TaskID+": ") + err.Error())
			continue
		}
		pruned++
	}
	verb := "Pruned"
	if dryRun {
		verb = "Would prune"
	}
	fmt.Println(instOK.Render(fmt.Sprintf("✓ %s %d task note file(s).", verb, pruned)))
	return nil
}
