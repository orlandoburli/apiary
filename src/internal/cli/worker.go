package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/queue"
	"github.com/orlandoburli/apiary/internal/queuehttp"
	"github.com/orlandoburli/apiary/internal/worker"
)

func newWorkerCmd() *cobra.Command {
	var controlPlane, token, workerID, pool string
	var labels, capabilities []string
	var capacity int
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run a distributed queue worker",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			if errs := cfg.Validate(); len(errs) > 0 {
				return fmt.Errorf("config validation failed: %v", errs)
			}
			if controlPlane == "" {
				controlPlane = queueControlPlaneURL(cfg.Settings.Queue.Listen)
			}
			if controlPlane == "" {
				return fmt.Errorf("--control-plane is required (for example http://apiary-control:8080)")
			}
			if token == "" {
				token = cfg.Settings.Queue.WorkerToken
			}
			if token == "" {
				return fmt.Errorf("--token or settings.queue.worker_token is required")
			}
			if workerID == "" {
				workerID = cfg.Settings.Queue.WorkerID
			}
			if workerID == "" {
				host, _ := os.Hostname()
				workerID = host
			}
			if strings.TrimSpace(workerID) == "" {
				return fmt.Errorf("--id is required when the hostname is unavailable")
			}
			if pool == "" {
				pool = cfg.Settings.Queue.WorkerPool
			}
			if pool == "" {
				pool = "default"
			}
			if capacity == 0 {
				capacity = cfg.Settings.Queue.WorkerCapacityValue()
			}
			labels = append(labels, cfg.Settings.Queue.WorkerLabels...)
			labels = append(labels, runtime.GOOS)
			capabilities = append(capabilities, cfg.Settings.Queue.WorkerCapabilities...)
			capabilities = append(capabilities, "apiary.workflow")
			for i := range cfg.Runners {
				capabilities = append(capabilities, "runner:"+cfg.Runners[i].AdapterName())
			}
			labels, capabilities = normalizedValues(labels), normalizedValues(capabilities)

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			workerDataDir := filepath.Join(projectDataDir(), "workers")
			if err := os.MkdirAll(workerDataDir, 0o700); err != nil {
				return fmt.Errorf("create worker data directory: %w", err)
			}
			workerDB, err := db.New(ctx, filepath.Join(workerDataDir, safeWorkerFilename(workerID)+".db"))
			if err != nil {
				return fmt.Errorf("open worker execution database: %w", err)
			}
			defer workerDB.Close()
			workerCfg := *cfg
			workerCfg.Settings = cfg.Settings
			disabled := false
			workerCfg.Settings.Queue.Enabled = &disabled
			dispatcher, err := daemon.New(ctx, &workerCfg, configFile, workerDB, nil, "")
			if err != nil {
				return fmt.Errorf("initialize worker runners: %w", err)
			}
			remote := &queuehttp.Client{BaseURL: controlPlane, Token: token}
			runtimeWorker, err := worker.New(remote, worker.ExecutorFunc(func(execCtx context.Context, job queue.Job) queue.FinishResult {
				return dispatcher.ExecuteQueuedJob(execCtx, job, workerID)
			}), worker.Config{
				Worker:        queue.Worker{ID: workerID, ProtocolVersion: queue.WorkerProtocolVersion, Pool: pool, Labels: labels, Capabilities: capabilities, Capacity: capacity, Ready: true},
				LeaseDuration: cfg.Settings.Queue.LeaseDurationValue(), HeartbeatInterval: cfg.Settings.Queue.HeartbeatIntervalValue(), WorkerHeartbeat: cfg.Settings.Queue.HeartbeatIntervalValue(), WorkerTimeout: cfg.Settings.Queue.WorkerTimeoutValue(), PollInterval: cfg.Settings.Queue.PollIntervalValue(),
				OnError: func(err error) { aplog.Error("remote worker: %v", err) },
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "apiary worker %s connected to %s (pool=%s capacity=%d)\n", workerID, controlPlane, pool, capacity)
			return runtimeWorker.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&controlPlane, "control-plane", "", "control-plane base URL")
	cmd.Flags().StringVar(&token, "token", "", "worker bearer token (defaults to settings.queue.worker_token)")
	cmd.Flags().StringVar(&workerID, "id", "", "stable worker id")
	cmd.Flags().StringVar(&pool, "pool", "", "worker pool")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "worker label (repeatable)")
	cmd.Flags().StringSliceVar(&capabilities, "capability", nil, "worker capability (repeatable)")
	cmd.Flags().IntVar(&capacity, "capacity", 0, "maximum concurrent jobs")
	return cmd
}

func queueControlPlaneURL(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	if strings.HasPrefix(listen, "http://") || strings.HasPrefix(listen, "https://") {
		return listen
	}
	if strings.HasPrefix(listen, ":") {
		return "http://127.0.0.1" + listen
	}
	if strings.HasPrefix(listen, "0.0.0.0:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(listen, "0.0.0.0:")
	}
	if strings.HasPrefix(listen, "[") || strings.Count(listen, ":") == 1 {
		return "http://" + listen
	}
	return listen
}

func normalizedValues(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func safeWorkerFilename(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
	if value == "" {
		return "worker"
	}
	return value
}
