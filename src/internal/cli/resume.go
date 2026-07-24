package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

func newResumeCmd() *cobra.Command {
	var (
		yes        bool
		workflow   string
		fromStep   string
		configMode string
	)

	cmd := &cobra.Command{
		Use:   "resume [instance-id]",
		Short: "Resume a failed or interrupted workflow instance",
		Long: "Replay a failed or interrupted workflow instance as a new immutable descendant. " +
			"Completed steps are copied from cache (not re-executed); the descendant continues from the failure point.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return runResume(id, workflow, fromStep, configMode, yes)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().StringVar(&workflow, "workflow", "", "resume the most recent failed/interrupted instance of this workflow")
	cmd.Flags().StringVar(&fromStep, "from", "", "re-run from this step instead of the first incomplete step")
	cmd.Flags().StringVar(&configMode, "definition", daemon.ResumeConfigCurrent, "workflow definition to use: current or original")
	return cmd
}

func runResume(id, workflow, fromStep, configMode string, yes bool) error {
	if (id == "") == (workflow == "") {
		fmt.Println(instErr.Render("Provide exactly one of <instance-id> or --workflow."))
		os.Exit(1)
	}
	if configMode != daemon.ResumeConfigCurrent && configMode != daemon.ResumeConfigOriginal {
		return fmt.Errorf("invalid --definition %q: expected current or original", configMode)
	}
	resumeQuery := url.Values{"config": {configMode}}
	if fromStep != "" {
		resumeQuery.Set("from", fromStep)
	}

	// Resolve the target instance from --workflow if no id was given.
	if workflow != "" {
		var res struct {
			InstanceID string `json:"instance_id"`
		}
		status, err := ipcDo(http.MethodGet, "/resume/?"+url.Values{"workflow": {workflow}}.Encode(), &res)
		if err != nil {
			return resumeFail(status, err, "workflow "+workflow)
		}
		id = res.InstanceID
	}

	// Preview (also validates resumability and maps exit codes).
	var preview daemon.ResumePreview
	status, err := ipcDo(http.MethodGet, "/resume/"+url.PathEscape(id)+"?"+resumeQuery.Encode(), &preview)
	if err != nil {
		return resumeFail(status, err, id)
	}

	if !yes {
		printResumePreview(preview)
		if !confirm("Proceed? [y/N] ") {
			fmt.Println(instMuted.Render("Aborted."))
			return nil
		}
	}

	// Execute.
	var queued struct {
		InstanceID string `json:"instance_id"`
	}
	status, err = ipcDo(http.MethodPost, "/resume/"+url.PathEscape(id)+"?"+resumeQuery.Encode(), &queued)
	if err != nil {
		return resumeFail(status, err, id)
	}
	fmt.Println(instOK.Render("✓") + " Resume queued as " + queued.InstanceID + " (from " + id + ").")
	return nil
}

func printResumePreview(p daemon.ResumePreview) {
	cell := p.CellID
	if p.Title != "" {
		cell += " — " + p.Title
	}
	fmt.Printf("\nResuming instance %s (%s / %s)\n\n", p.InstanceID, p.Workflow, cell)
	fmt.Printf("Configuration: %s", p.ConfigMode)
	if p.FromStep != "" {
		fmt.Printf("  ·  from step: %s", p.FromStep)
	}
	fmt.Println()

	if len(p.Skip) > 0 {
		fmt.Println(instHeader.Render("Steps to skip (already completed):"))
		for _, s := range p.Skip {
			note := ""
			if s.Note != "" {
				note = instMuted.Render(" — " + s.Note)
			}
			fmt.Printf("  %s %-12s %-14s%s\n", instOK.Render("✓"), s.StepID, s.Agent, note)
		}
		fmt.Println()
	}

	fmt.Println(instHeader.Render("Steps to run:"))
	for _, s := range p.Run {
		fmt.Printf("  %s %-12s %s\n", instMuted.Render("○"), s.StepID, s.Agent)
	}
	fmt.Println()
}

// resumeFail prints a message and exits with the code mapped from the IPC status.
// A status of 0 means a transport failure (daemon not reachable).
func resumeFail(status int, err error, subject string) error {
	switch status {
	case http.StatusNotFound:
		fmt.Println(instErr.Render("Not found: ") + subject)
		os.Exit(2)
	case http.StatusConflict:
		fmt.Println(instErr.Render("Not resumable: ") + subject +
			instMuted.Render(" (only failed or interrupted instances can be resumed)"))
		os.Exit(3)
	case http.StatusUnprocessableEntity:
		fmt.Println(instErr.Render("Workflow definition changed or removed for ") + subject)
		os.Exit(4)
	case 0:
		return daemonDownHint()
	default:
		fmt.Println(instErr.Render("Error: ") + err.Error())
		os.Exit(1)
	}
	return nil
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// ipcDo performs an HTTP request against the daemon's Unix socket and decodes a
// JSON response into out (when non-nil). It returns the HTTP status code (0 on
// transport failure) so callers can map distinct exit codes.
func ipcDo(method, path string, out any) (int, error) {
	dataDir := config.DataDir(configFile)
	socketPath := daemon.SocketPath(dataDir)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	req, err := http.NewRequest(method, "http://apiary"+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Apiary-Control", daemon.ReadControlToken(dataDir))
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
