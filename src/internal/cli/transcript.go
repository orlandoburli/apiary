package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newTranscriptCmd() *cobra.Command {
	var (
		last     bool
		pathOnly bool
	)

	cmd := &cobra.Command{
		Use:   "transcript [task-id] [file]",
		Short: "Browse markdown transcripts of agent sessions",
		Long: `Browse the markdown transcripts apiary records for each workflow step run
(assistant messages, thinking, tool calls and results).

  apiary transcript                    list tasks that have transcripts
  apiary transcript <task-id>          list that task's transcript files
  apiary transcript <task-id> --last   print the most recent transcript
  apiary transcript <task-id> <file>   print a specific transcript

Transcripts live under <config-dir>/.apiary/logs/transcripts/ and are written
live, so you can also 'tail -f' one while the agent is running.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := filepath.Join(getLogDir(), "transcripts")
			switch len(args) {
			case 0:
				return listTranscriptTasks(root)
			case 1:
				if last {
					return printTranscript(latestTranscript(filepath.Join(root, args[0])), pathOnly)
				}
				return listTranscriptFiles(filepath.Join(root, args[0]))
			default:
				name := args[1]
				if !strings.HasSuffix(name, ".md") {
					name += ".md"
				}
				return printTranscript(filepath.Join(root, args[0], name), pathOnly)
			}
		},
	}

	cmd.Flags().BoolVar(&last, "last", false, "print the most recent transcript for the task")
	cmd.Flags().BoolVar(&pathOnly, "path", false, "print the file path instead of its contents")
	return cmd
}

func listTranscriptTasks(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) || (err == nil && len(entries) == 0) {
		fmt.Println("no transcripts yet — they are recorded as workflow steps run")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println(instHeader.Render("TASK                                     FILES  LAST ACTIVITY"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, e.Name(), "*.md"))
		latest := latestOf(files)
		mod := ""
		if fi, err := os.Stat(latest); err == nil {
			mod = fi.ModTime().Format("2006-01-02 15:04")
		}
		fmt.Printf("%-40s %5d  %s\n", e.Name(), len(files), instMuted.Render(mod))
	}
	return nil
}

func listTranscriptFiles(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(files) == 0 {
		fmt.Printf("no transcripts for task %q\n", filepath.Base(dir))
		return nil
	}
	sortByModTime(files)
	for _, f := range files {
		mod := ""
		if fi, err := os.Stat(f); err == nil {
			mod = fi.ModTime().Format("2006-01-02 15:04")
		}
		fmt.Printf("%s  %s\n", instMuted.Render(mod), filepath.Base(f))
	}
	return nil
}

func printTranscript(path string, pathOnly bool) error {
	if path == "" {
		return fmt.Errorf("no transcript found")
	}
	if pathOnly {
		fmt.Println(path)
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func latestTranscript(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	return latestOf(files)
}

func latestOf(files []string) string {
	if len(files) == 0 {
		return ""
	}
	sortByModTime(files)
	return files[len(files)-1]
}

func sortByModTime(files []string) {
	sort.Slice(files, func(i, j int) bool {
		fi, err1 := os.Stat(files[i])
		fj, err2 := os.Stat(files[j])
		if err1 != nil || err2 != nil {
			return files[i] < files[j]
		}
		return fi.ModTime().Before(fj.ModTime())
	})
}
