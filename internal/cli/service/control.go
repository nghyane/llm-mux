package service

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

func runServiceCommand(action string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		// macOS uses launchctl
		serviceName := "com.llm-mux"
		switch action {
		case "start":
			cmd = exec.Command("launchctl", "start", serviceName)
		case "stop":
			cmd = exec.Command("launchctl", "stop", serviceName)
		case "status":
			// launchctl list returns 0 if running, non-zero if not
			cmd = exec.Command("launchctl", "list", serviceName)
		}
	case "linux":
		// Linux uses systemctl --user
		serviceName := "llm-mux"
		switch action {
		case "start":
			cmd = exec.Command("systemctl", "--user", "start", serviceName)
		case "stop":
			cmd = exec.Command("systemctl", "--user", "stop", serviceName)
		case "status":
			cmd = exec.Command("systemctl", "--user", "is-active", serviceName)
		}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	if cmd == nil {
		return fmt.Errorf("unknown action: %s", action)
	}

	// Capture output for status check, otherwise stream to stdout
	if action == "status" {
		return cmd.Run()
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceCommand("start")
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceCommand("stop")
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			_ = runServiceCommand("stop")
			return runServiceCommand("start")
		}
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check service status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := runServiceCommand("status"); err != nil {
			fmt.Println("Service is stopped")
			return nil
		}
		fmt.Println("Service is running")
		return nil
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View service logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		var c *exec.Cmd
		if runtime.GOOS == "darwin" {
			home, _ := os.UserHomeDir()
			logPath := home + "/.local/var/log/llm-mux.log"
			c = exec.Command("tail", "-f", logPath)
		} else if runtime.GOOS == "linux" {
			c = exec.Command("journalctl", "--user", "-u", "llm-mux", "-f")
		} else {
			return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
		}

		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	ServiceCmd.AddCommand(startCmd)
	ServiceCmd.AddCommand(stopCmd)
	ServiceCmd.AddCommand(restartCmd)
	ServiceCmd.AddCommand(statusCmd)
	ServiceCmd.AddCommand(logsCmd)
}
