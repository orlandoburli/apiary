package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// program satisfies service.Interface. It is intentionally inert: kardianos is
// used only to write, load and query the service definition. The process the
// service manager supervises is a plain `apiary run`, which handles SIGTERM
// itself (drains active runs, then exits) — it never goes through service.Run.
type program struct{}

func (p *program) Start(s service.Service) error { return nil }
func (p *program) Stop(s service.Service) error  { return nil }

const (
	// defaultServiceName names the per-user service. It deliberately differs
	// from legacyServiceName so the two are never confused in `launchctl list`
	// or `systemctl status` output.
	defaultServiceName = "apiary"

	// legacyServiceName is the system-level service that `apiary service
	// install` used to write. It had no arguments, so it ran bare `apiary` as
	// root in a respawn loop and never started the daemon.
	legacyServiceName = "apiary-dispatcher"

	// defaultStopTimeout is how long the service manager waits after SIGTERM
	// before killing the daemon. The daemon drains active agent runs on
	// SIGTERM, which can legitimately take minutes.
	defaultStopTimeout = 10 * time.Minute

	// restartDelaySeconds throttles respawns after an unsuccessful exit, so a
	// daemon that cannot start (bad config, missing token) does not spin.
	restartDelaySeconds = 30
)

// serviceInstallOptions is everything captured at install time. The service
// manager starts the daemon with none of the invoking shell's context, so the
// config path, working directory and PATH must all be baked into the definition.
type serviceInstallOptions struct {
	Name        string
	Executable  string // empty = the running binary
	ConfigPath  string // absolute
	WorkDir     string // absolute; the daemon loads .env relative to it
	EnvFile     string // absolute; empty = the daemon's default (.env in WorkDir)
	Profile     string
	PathEnv     string
	LogDir      string // launchd stdout/stderr files; systemd uses the journal
	StopTimeout time.Duration
}

// launchdTemplate replaces the kardianos default, which cannot express
// KeepAlive/SuccessfulExit, ExitTimeOut or ThrottleInterval. %d verbs are
// filled by buildServiceConfig; the rest is rendered by kardianos at install.
const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{html .Name}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{html .Path}}</string>
		{{- range .Config.Arguments}}
		<string>{{html .}}</string>
		{{- end}}
	</array>
	<key>WorkingDirectory</key>
	<string>{{html .WorkingDirectory}}</string>
	{{- if .EnvVars}}
	<key>EnvironmentVariables</key>
	<dict>
		{{- range $k, $v := .EnvVars}}
		<key>{{html $k}}</key>
		<string>{{html $v}}</string>
		{{- end}}
	</dict>
	{{- end}}
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>%d</integer>
	<key>ExitTimeOut</key>
	<integer>%d</integer>
	<key>ProcessType</key>
	<string>Background</string>
	{{- if .StandardOutPath}}
	<key>StandardOutPath</key>
	<string>{{html .StandardOutPath}}</string>
	{{- end}}
	{{- if .StandardErrorPath}}
	<key>StandardErrorPath</key>
	<string>{{html .StandardErrorPath}}</string>
	{{- end}}
</dict>
</plist>
`

// systemdTemplate is a *user* unit. KillMode=mixed matters: the default
// (control-group) would SIGTERM every agent CLI in the cgroup at once, turning
// a graceful drain into a mass kill.
const systemdTemplate = `[Unit]
Description={{.Description}}
ConditionFileIsExecutable={{.Path|cmdEscape}}

[Service]
ExecStart={{.Path|cmdEscape}}{{range .Arguments}} {{.|cmd}}{{end}}
WorkingDirectory={{.WorkingDirectory|cmdEscape}}
{{range $k, $v := .EnvVars -}}
Environment="{{$k}}={{$v}}"
{{end -}}
Restart=on-failure
RestartSec=%d
KillMode=mixed
TimeoutStopSec=%d

[Install]
WantedBy=default.target
`

// buildServiceConfig turns install options into the kardianos config. It is
// pure (no filesystem, no environment) so the generated definition is testable.
func buildServiceConfig(o serviceInstallOptions, goos string) *service.Config {
	args := []string{"run", "--config", o.ConfigPath}
	if o.EnvFile != "" {
		args = append(args, "--env-file", o.EnvFile)
	}
	if o.Profile != "" {
		args = append(args, "--profile", o.Profile)
	}

	env := map[string]string{}
	if o.PathEnv != "" {
		env["PATH"] = o.PathEnv
		if goos == "linux" {
			env["PATH"] = systemdEnvEscape(o.PathEnv)
		}
	}

	stopSecs := int(o.StopTimeout / time.Second)

	return &service.Config{
		Name:             o.Name,
		DisplayName:      "Apiary",
		Description:      "Apiary daemon (" + o.ConfigPath + ")",
		Executable:       o.Executable,
		Arguments:        args,
		WorkingDirectory: o.WorkDir,
		EnvVars:          env,
		Option: service.KeyValue{
			// Run as the invoking user, never root: agent CLIs need the
			// user's HOME, keychain and credentials (see settings.refuse_root).
			"UserService":   true,
			"LogDirectory":  o.LogDir,
			"LaunchdConfig": fmt.Sprintf(launchdTemplate, restartDelaySeconds, stopSecs),
			"SystemdScript": fmt.Sprintf(systemdTemplate, restartDelaySeconds, stopSecs),
		},
	}
}

// dedupePathList drops empty and repeated entries from a PATH-style list,
// keeping first occurrences so lookup order is unchanged. Shell profiles tend
// to prepend the same directories many times over.
func dedupePathList(p string) string {
	seen := map[string]bool{}
	var out []string
	for _, e := range filepath.SplitList(p) {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// systemdEnvEscape makes a value safe inside a quoted Environment= assignment:
// `%` starts a systemd specifier, and `\` / `"` are quoting characters.
func systemdEnvEscape(v string) string {
	return strings.NewReplacer(`%`, `%%`, `\`, `\\`, `"`, `\"`).Replace(v)
}

// controlServiceConfig is the minimal config for start/stop/status/uninstall,
// which only need the name and the user-service scope to find the definition.
func controlServiceConfig(name string) *service.Config {
	return &service.Config{
		Name:   name,
		Option: service.KeyValue{"UserService": true},
	}
}

// serviceSupported rejects platforms where the installed service could not
// work: a Windows Service must speak the SCM protocol, which `apiary run` does
// not.
func serviceSupported(goos string) error {
	switch goos {
	case "darwin", "linux":
		return nil
	}
	return fmt.Errorf("apiary service is not supported on %s: run `apiary run` under your own supervisor (on Windows, Task Scheduler or NSSM)", goos)
}

// legacyServicePath returns where the old system-level install lives on goos.
func legacyServicePath(goos string) string {
	switch goos {
	case "darwin":
		return "/Library/LaunchDaemons/" + legacyServiceName + ".plist"
	case "linux":
		return "/etc/systemd/system/" + legacyServiceName + ".service"
	}
	return ""
}

// legacyServiceWarning describes how to remove the old broken system-level
// install, or returns "" when exists reports it is not there. Removal needs
// root, so it is only ever printed, never attempted.
func legacyServiceWarning(goos string, exists func(string) bool) string {
	path := legacyServicePath(goos)
	if path == "" || !exists(path) {
		return ""
	}
	var cmds string
	switch goos {
	case "darwin":
		cmds = "  sudo launchctl bootout system/" + legacyServiceName + "\n" +
			"  sudo rm " + path + "\n"
	case "linux":
		cmds = "  sudo systemctl disable --now " + legacyServiceName + "\n" +
			"  sudo rm " + path + "\n" +
			"  sudo systemctl daemon-reload\n"
	}
	return "warning: an old system-level service definition exists at " + path + ".\n" +
		"It was written by an earlier `apiary service install`, never ran the daemon, and\n" +
		"respawns a bare `apiary` as root. Remove it (needs sudo):\n\n" + cmds
}

func warnLegacyService() {
	msg := legacyServiceWarning(runtime.GOOS, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
}

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the Apiary daemon as a per-user background service",
		Long: "Install `apiary run` for the current project as a per-user service\n" +
			"(launchd LaunchAgent on macOS, systemd user unit on Linux).\n\n" +
			"The service runs as you, not root: agent CLIs need your HOME, keychain and PATH.",
	}
	cmd.PersistentFlags().String("name", defaultServiceName, "service name (use distinct names to run several projects)")

	cmd.AddCommand(
		newServiceInstallCmd(),
		newServiceUninstallCmd(),
		newServiceStartCmd(),
		newServiceStopCmd(),
		newServiceRestartCmd(),
		newServiceStatusCmd(),
	)
	return cmd
}

// controlService builds the service handle shared by every subcommand except
// install.
func controlService(cmd *cobra.Command) (service.Service, string, error) {
	if err := serviceSupported(runtime.GOOS); err != nil {
		return nil, "", err
	}
	name, _ := cmd.Flags().GetString("name")
	svc, err := service.New(&program{}, controlServiceConfig(name))
	return svc, name, err
}

func newServiceInstallCmd() *cobra.Command {
	var (
		workDir, pathEnv, executable, profile string
		stopTimeout                           time.Duration
		force                                 bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install `apiary run` for this project as a per-user service",
		Long: "Writes a launchd LaunchAgent (macOS) or systemd user unit (Linux) that runs\n" +
			"`apiary run --config <config>` from the project directory as the current user.\n\n" +
			"The config path (--config), working directory and PATH are captured now,\n" +
			"because the service manager starts the daemon without your shell's environment.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := serviceSupported(runtime.GOOS); err != nil {
				return err
			}
			warnLegacyService()
			if os.Geteuid() == 0 {
				return errors.New("refusing to install as root: the service must run as the user who owns the agent CLI credentials — run `apiary service install` without sudo")
			}

			name, _ := cmd.Flags().GetString("name")
			opts := serviceInstallOptions{
				Name:        name,
				Executable:  executable,
				Profile:     profile,
				PathEnv:     pathEnv,
				StopTimeout: stopTimeout,
			}
			if opts.PathEnv == "" {
				opts.PathEnv = dedupePathList(os.Getenv("PATH"))
			}
			if stopTimeout < time.Second {
				return fmt.Errorf("--stop-timeout must be at least 1s, got %s", stopTimeout)
			}

			var err error
			if opts.WorkDir, err = filepath.Abs(workDir); err != nil {
				return err
			}
			if st, err := os.Stat(opts.WorkDir); err != nil || !st.IsDir() {
				return fmt.Errorf("working directory %s is not a directory", opts.WorkDir)
			}
			if opts.ConfigPath, err = filepath.Abs(configFile); err != nil {
				return err
			}
			if _, err := os.Stat(opts.ConfigPath); err != nil {
				return fmt.Errorf("config file %s not found: run from the project directory or pass --config", opts.ConfigPath)
			}
			if f := cmd.Root().PersistentFlags().Lookup("env-file"); f != nil && f.Changed {
				if opts.EnvFile, err = filepath.Abs(f.Value.String()); err != nil {
					return err
				}
			}
			if opts.Executable != "" {
				if opts.Executable, err = filepath.Abs(opts.Executable); err != nil {
					return err
				}
			}
			opts.LogDir = getLogDir()
			if opts.LogDir, err = filepath.Abs(opts.LogDir); err != nil {
				return err
			}
			if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
				return fmt.Errorf("create log dir: %w", err)
			}

			svc, err := service.New(&program{}, buildServiceConfig(opts, runtime.GOOS))
			if err != nil {
				return err
			}
			if force {
				if st, _ := svc.Status(); st == service.StatusRunning {
					if err := stopAndWait(svc, name, opts.StopTimeout+time.Minute); err != nil {
						return err
					}
				}
				_ = svc.Uninstall()
			}
			if err := svc.Install(); err != nil {
				return fmt.Errorf("install: %w (use --force to replace an existing definition)", err)
			}

			fmt.Printf("✓ service %q installed for user (not started)\n", name)
			fmt.Printf("  config:   %s\n", opts.ConfigPath)
			fmt.Printf("  workdir:  %s\n", opts.WorkDir)
			fmt.Printf("  PATH:     %s\n", opts.PathEnv)
			fmt.Printf("  stop timeout: %s\n", opts.StopTimeout)
			if runtime.GOOS == "linux" {
				fmt.Println("  note: to keep it running while you are logged out: loginctl enable-linger $USER")
			}
			fmt.Println("Start it with: apiary service start")
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "workdir", ".", "working directory for the daemon (where .env lives)")
	cmd.Flags().StringVar(&pathEnv, "path", "", "PATH for the daemon; must resolve the agent CLIs (default: the current $PATH)")
	cmd.Flags().StringVar(&executable, "executable", "", "apiary binary to run (default: the running binary)")
	cmd.Flags().StringVar(&profile, "profile", "", "runner profile passed to apiary run --profile")
	cmd.Flags().DurationVar(&stopTimeout, "stop-timeout", defaultStopTimeout, "how long the service manager lets the daemon drain active runs after SIGTERM before killing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing definition (stops the running service first)")
	return cmd
}

func newServiceUninstallCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the Apiary user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, name, err := controlService(cmd)
			if err != nil {
				return err
			}
			// kardianos does not stop a systemd unit on uninstall, and its
			// launchd unload does not report on the drain.
			if st, _ := svc.Status(); st == service.StatusRunning {
				if err := stopAndWait(svc, name, timeout); err != nil {
					return err
				}
			}
			if err := svc.Uninstall(); err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			fmt.Println("✓ service uninstalled")
			return nil
		},
	}
	addStopTimeoutFlag(cmd, &timeout)
	return cmd
}

func newServiceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Apiary service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, _, err := controlService(cmd)
			if err != nil {
				return err
			}
			warnLegacyService()
			if err := svc.Start(); err != nil {
				return fmt.Errorf("start: %w", err)
			}
			fmt.Println("✓ service started")
			return nil
		},
	}
}

func newServiceStopCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the Apiary service and wait for the daemon to drain active runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, name, err := controlService(cmd)
			if err != nil {
				return err
			}
			if err := stopAndWait(svc, name, timeout); err != nil {
				return err
			}
			fmt.Println("✓ service stopped")
			return nil
		},
	}
	addStopTimeoutFlag(cmd, &timeout)
	return cmd
}

func newServiceRestartCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop the service, wait for the drain, then start it again",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, name, err := controlService(cmd)
			if err != nil {
				return err
			}
			if st, _ := svc.Status(); st == service.StatusRunning {
				if err := stopAndWait(svc, name, timeout); err != nil {
					return err
				}
			}
			if err := svc.Start(); err != nil {
				return fmt.Errorf("start: %w", err)
			}
			fmt.Println("✓ service restarted")
			return nil
		},
	}
	addStopTimeoutFlag(cmd, &timeout)
	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, _, err := controlService(cmd)
			if err != nil {
				return err
			}
			warnLegacyService()
			status, err := svc.Status()
			if errors.Is(err, service.ErrNotInstalled) {
				fmt.Println("not installed")
				return nil
			}
			if err != nil {
				return err
			}
			switch status {
			case service.StatusRunning:
				fmt.Println("running")
			case service.StatusStopped:
				fmt.Println("stopped")
			default:
				fmt.Println("unknown")
			}
			return nil
		},
	}
}

func addStopTimeoutFlag(cmd *cobra.Command, timeout *time.Duration) {
	cmd.Flags().DurationVar(timeout, "timeout", defaultStopTimeout+time.Minute,
		"how long to wait for the daemon to drain active runs and exit")
}

// stopAndWait asks the service manager to stop the daemon, then waits for the
// daemon process itself to exit. The manager only delivers SIGTERM; the daemon
// then drains active runs, which can take minutes, and "stopped" must not be
// reported (nor a restart attempted) while it is still holding the database.
func stopAndWait(svc service.Service, name string, timeout time.Duration) error {
	pid := servicePID(runtime.GOOS, name)
	if err := svc.Stop(); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if pid <= 0 {
		return nil
	}
	return waitForExit(pid, timeout, func() {
		fmt.Fprintf(os.Stderr, "waiting for the daemon (pid %d) to drain active runs…\n", pid)
	})
}

// waitForExit polls until pid is gone. notify fires once, only if the process
// outlives the first check, so an idle daemon stops silently.
func waitForExit(pid int, timeout time.Duration, notify func()) error {
	deadline := time.Now().Add(timeout)
	notified := false
	for processAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon (pid %d) is still draining after %s; it keeps shutting down in the background — check again with `apiary service status`", pid, timeout)
		}
		if !notified {
			notified = true
			notify()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	// EPERM means the process exists but belongs to someone else.
	return err == nil || errors.Is(err, os.ErrPermission)
}

var launchctlPIDRe = regexp.MustCompile(`"PID" = ([0-9]+);`)

// servicePID asks the service manager for the daemon's pid; 0 when it is not
// running or cannot be determined.
func servicePID(goos, name string) int {
	var out []byte
	switch goos {
	case "darwin":
		out, _ = exec.Command("launchctl", "list", name).Output()
		return parseLaunchctlPID(string(out))
	case "linux":
		out, _ = exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", name+".service").Output()
		pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return pid
	}
	return 0
}

func parseLaunchctlPID(out string) int {
	m := launchctlPIDRe.FindStringSubmatch(out)
	if len(m) != 2 {
		return 0
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}
