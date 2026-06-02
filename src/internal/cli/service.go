package cli

import (
	"fmt"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

type program struct{}

func (p *program) Start(s service.Service) error { return nil }
func (p *program) Stop(s service.Service) error  { return nil }

var svcConfig = &service.Config{
	Name:        "apiary-dispatcher",
	DisplayName: "Apiary Dispatcher",
	Description: "Apiary background task dispatcher and scheduler",
}

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the Apiary background service",
	}

	cmd.AddCommand(
		newServiceInstallCmd(),
		newServiceUninstallCmd(),
		newServiceStartCmd(),
		newServiceStopCmd(),
		newServiceStatusCmd(),
	)
	return cmd
}

func newServiceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install Apiary as a system service (systemd/launchd/Windows Service)",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.New(&program{}, svcConfig)
			if err != nil {
				return err
			}
			if err := svc.Install(); err != nil {
				return fmt.Errorf("install: %w", err)
			}
			fmt.Println("✓ service installed")
			return nil
		},
	}
}

func newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the Apiary system service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.New(&program{}, svcConfig)
			if err != nil {
				return err
			}
			if err := svc.Uninstall(); err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			fmt.Println("✓ service uninstalled")
			return nil
		},
	}
}

func newServiceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Apiary service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.New(&program{}, svcConfig)
			if err != nil {
				return err
			}
			if err := svc.Start(); err != nil {
				return fmt.Errorf("start: %w", err)
			}
			fmt.Println("✓ service started")
			return nil
		},
	}
}

func newServiceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Apiary service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.New(&program{}, svcConfig)
			if err != nil {
				return err
			}
			if err := svc.Stop(); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
			fmt.Println("✓ service stopped")
			return nil
		},
	}
}

func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.New(&program{}, svcConfig)
			if err != nil {
				return err
			}
			status, err := svc.Status()
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
